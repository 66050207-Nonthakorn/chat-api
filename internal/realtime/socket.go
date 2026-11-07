package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/strictjson"
	"cloudchat/internal/uuid"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
)

type socket struct {
	server    *Server
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	principal auth.Principal
	requestID string

	mu                  sync.Mutex
	rooms               map[string]struct{}
	stopOnce            sync.Once
	outbound            *messageQueue
	rateLimitViolations int
}

func (c *socket) readLoop() {
	for {
		messageType, payload, err := c.conn.Read(c.ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			if status == -1 {
				status = websocket.StatusNormalClosure
			}
			c.stop(status, "")
			return
		}
		process, keepOpen := c.checkCommandRateLimit(payload)
		if !keepOpen {
			return
		}
		if !process {
			continue
		}
		if !c.checkSessionResult(c.server.sessionValid(c.ctx, c.principal)) {
			return
		}
		if !c.handleCommand(messageType, payload) {
			return
		}
	}
}

func (c *socket) checkCommandRateLimit(payload []byte) (process bool, keepOpen bool) {
	limiter := c.server.options.RateLimiter
	if limiter == nil {
		if c.server.config.Environment == config.EnvironmentLocal {
			return true, true
		}
		c.stop(websocket.StatusTryAgainLater, "")
		return false, false
	}
	identity := c.principal.UserID + ":" + c.principal.SessionID + ":" + c.requestID
	decision, err := limiter.Allow(c.ctx, ratelimit.WebSocketCommand, identity)
	if err != nil {
		c.stop(websocket.StatusTryAgainLater, "")
		return false, false
	}
	if decision.Allowed {
		return true, true
	}
	c.rateLimitViolations++
	if c.rateLimitViolations > 1 {
		c.stop(websocket.StatusPolicyViolation, "rate limit exceeded")
		return false, false
	}
	var envelope struct {
		RequestID *string `json:"request_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.RequestID == nil || !uuid.IsCanonical(*envelope.RequestID) {
		envelope.RequestID = nil
	}
	c.respondErrorAt(envelope.RequestID, "RATE_LIMITED", "WebSocket command rate limit exceeded", true, time.Now())
	return false, c.ctx.Err() == nil
}

func (c *socket) handleCommand(messageType websocket.MessageType, payload []byte) bool {
	startedAt := time.Now()
	if messageType != websocket.MessageText {
		c.stop(websocket.StatusPolicyViolation, "text JSON frames required")
		return false
	}
	var command clientCommand
	if err := strictjson.Decode(payload, &command); err != nil {
		c.stop(websocket.StatusPolicyViolation, "invalid command")
		return false
	}
	if command.RequestID == nil || !uuid.IsCanonical(*command.RequestID) {
		c.respondErrorAt(nil, "INVALID_REQUEST", "Request ID must be a UUID", false, startedAt)
		return true
	}
	switch command.Type {
	case "subscribe":
		if !uuid.IsCanonical(command.RoomID) {
			c.respondErrorAt(command.RequestID, "INVALID_REQUEST", "Room ID must be a UUID", false, startedAt)
			return true
		}
		return c.subscribe(*command.RequestID, command.RoomID, startedAt)
	case "unsubscribe":
		if !uuid.IsCanonical(command.RoomID) {
			c.respondErrorAt(command.RequestID, "INVALID_REQUEST", "Room ID must be a UUID", false, startedAt)
			return true
		}
		return c.unsubscribe(*command.RequestID, command.RoomID, startedAt)
	case "ping":
		if !c.checkSessionResult(c.server.touchSessionIdle(c.ctx, c.principal)) {
			return false
		}
		return c.respondAcknowledgement(struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}{Type: "pong", RequestID: *command.RequestID}, "ping", "queued", startedAt)
	default:
		c.respondErrorAt(command.RequestID, "INVALID_REQUEST", "Unknown WebSocket command", false, startedAt)
		return true
	}
}

func (s *Server) touchSessionIdle(parent context.Context, principal auth.Principal) (bool, error) {
	if principal.SessionID == "" {
		return s.sessionValid(parent, principal)
	}
	return s.sessionRules.TouchIdle(parent, principal)
}

func (c *socket) subscribe(requestID, roomID string, startedAt time.Time) bool {
	allowed, err := c.server.hasMembership(c.ctx, roomID, c.principal.UserID)
	if err != nil {
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	if !allowed {
		c.respondErrorAt(&requestID, "ROOM_NOT_FOUND", "Room unavailable", false, startedAt)
		return true
	}

	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return false
	}
	_, subscribed := c.rooms[roomID]
	if !subscribed && len(c.rooms) >= maxSubscriptionsPerSocket {
		c.mu.Unlock()
		c.respondErrorAt(&requestID, "SUBSCRIPTION_LIMIT", "Too many room subscriptions", false, startedAt)
		return true
	}
	if !subscribed {
		if !c.server.addSubscription(c, roomID) {
			c.mu.Unlock()
			c.stop(websocket.StatusGoingAway, "")
			return false
		}
		c.rooms[roomID] = struct{}{}
	}
	sequence, err := c.server.roomLastSequence(c.ctx, roomID, c.principal.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		if !subscribed {
			delete(c.rooms, roomID)
			c.server.removeSubscription(c, roomID)
		}
		c.mu.Unlock()
		c.respondErrorAt(&requestID, "ROOM_NOT_FOUND", "Room unavailable", false, startedAt)
		return true
	}
	if err != nil {
		c.mu.Unlock()
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	frame, _ := marshalFrame(outboundFrame{
		roomID:  roomID,
		private: true,
		value: struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			RoomID    string `json:"room_id"`
			LastSeq   string `json:"last_seq"`
		}{Type: "subscribed", RequestID: requestID, RoomID: roomID, LastSeq: sequence},
	})
	queued := c.outbound.push(frame)
	c.mu.Unlock()
	if !queued {
		c.logAcknowledgement("subscribe", "failed", startedAt)
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	c.logAcknowledgement("subscribe", "queued", startedAt)
	return true
}

func (c *socket) unsubscribe(requestID, roomID string, startedAt time.Time) bool {
	c.mu.Lock()
	if _, exists := c.rooms[roomID]; exists {
		delete(c.rooms, roomID)
		c.server.removeSubscription(c, roomID)
		c.outbound.dropMessageEvents(roomID)
	}
	frame, _ := marshalFrame(outboundFrame{value: struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		RoomID    string `json:"room_id"`
	}{Type: "unsubscribed", RequestID: requestID, RoomID: roomID}})
	queued := c.outbound.push(frame)
	c.mu.Unlock()
	if !queued {
		c.logAcknowledgement("unsubscribe", "failed", startedAt)
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	c.logAcknowledgement("unsubscribe", "queued", startedAt)
	return true
}

func (c *socket) respondErrorAt(requestID *string, code, message string, retryable bool, startedAt time.Time) {
	c.respondAcknowledgement(protocolErrorFrame{
		Type:      "error",
		RequestID: requestID,
		Error: protocolError{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	}, "error", "error", startedAt)
}

func (c *socket) respondAcknowledgement(value any, ackType, state string, startedAt time.Time) bool {
	frame, err := marshalFrame(outboundFrame{value: value})
	if err != nil || !c.outbound.push(frame) {
		c.logAcknowledgement(ackType, "failed", startedAt)
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	c.logAcknowledgement(ackType, state, startedAt)
	return true
}

func (c *socket) logAcknowledgement(ackType, state string, startedAt time.Time) {
	c.server.logger.Info("websocket command acknowledgement enqueued", "event", "socket.ack", "service", "api",
		"request_id", c.requestID, "ack_type", ackType, "state", state,
		"enqueue_duration_ms", time.Since(startedAt).Milliseconds())
}

func (c *socket) deliver(frame outboundFrame) {
	c.mu.Lock()
	if _, subscribed := c.rooms[frame.roomID]; !subscribed || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	queued := c.outbound.push(frame)
	c.mu.Unlock()
	if !queued {
		c.stop(websocket.StatusTryAgainLater, "")
	}
}

func (c *socket) writeLoop() {
	for {
		frame, ok := c.outbound.pop(c.ctx)
		if !ok {
			return
		}
		if frame.private {
			if !c.writePrivate(frame) {
				return
			}
			continue
		}
		if err := c.write(frame.payload); err != nil {
			c.stop(websocket.StatusGoingAway, "")
			return
		}
	}
}

func (c *socket) writePrivate(frame outboundFrame) bool {
	c.mu.Lock()
	if _, subscribed := c.rooms[frame.roomID]; !subscribed || c.ctx.Err() != nil {
		c.mu.Unlock()
		return true
	}
	valid, err := c.server.sessionValid(c.ctx, c.principal)
	if err != nil {
		c.mu.Unlock()
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	if !valid {
		c.mu.Unlock()
		c.stop(4401, "")
		return false
	}
	allowed, err := c.server.hasMembership(c.ctx, frame.roomID, c.principal.UserID)
	if err != nil {
		c.mu.Unlock()
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	if !allowed {
		queued := c.revokeLocked(frame.roomID)
		c.mu.Unlock()
		if !queued {
			c.stop(websocket.StatusTryAgainLater, "")
			return false
		}
		return true
	}
	err = c.write(frame.payload)
	c.mu.Unlock()
	if err != nil {
		c.stop(websocket.StatusGoingAway, "")
		return false
	}
	return true
}

func (c *socket) revokeLocked(roomID string) bool {
	delete(c.rooms, roomID)
	c.server.removeSubscription(c, roomID)
	c.outbound.dropMessageEvents(roomID)
	frame, _ := marshalFrame(outboundFrame{roomID: roomID, value: struct {
		Type   string `json:"type"`
		RoomID string `json:"room_id"`
		Reason string `json:"reason"`
	}{Type: "subscription.revoked", RoomID: roomID, Reason: "ACCESS_REVOKED"}})
	return c.outbound.push(frame)
}

func (c *socket) write(payload []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, c.server.options.WriteTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, payload)
}

func (c *socket) monitorSession() {
	ticker := time.NewTicker(c.server.options.SessionRecheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if !c.checkSessionResult(c.server.sessionValid(c.ctx, c.principal)) {
				return
			}
		}
	}
}

func (c *socket) checkSessionResult(valid bool, err error) bool {
	if err != nil {
		c.stop(websocket.StatusTryAgainLater, "")
		return false
	}
	if !valid {
		c.stop(4401, "")
		return false
	}
	return true
}

func (c *socket) stop(code websocket.StatusCode, reason string) {
	c.stopOnce.Do(func() {
		c.server.removeSocket(c, code)
		c.outbound.close()
		closed := make(chan struct{})
		go func() {
			_ = c.conn.Close(code, reason)
			close(closed)
		}()
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-closed:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = c.conn.CloseNow()
			<-closed
		}
		c.cancel()
	})
}
