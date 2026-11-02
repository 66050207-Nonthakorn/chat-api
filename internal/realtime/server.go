package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloudchat/internal/apierror"
	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	chatmessage "cloudchat/internal/message"
	"cloudchat/internal/outbox"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/requestid"
	"cloudchat/internal/strictjson"
	"cloudchat/internal/uuid"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	webSocketProtocol         = "chat.v1"
	maxSubscriptionsPerSocket = 50
	defaultQueueFrames        = 256
	defaultQueueBytes         = 1 << 20
	defaultReadLimit          = 16 << 10
	defaultWriteTimeout       = 10 * time.Second
	defaultSessionRecheck     = 25 * time.Second
	queryTimeout              = 5 * time.Second
)

// Options contains explicit resource limits. Smaller values are useful for
// exercising overload behavior through public WebSocket clients.
type Options struct {
	QueueFrames            int
	QueueBytes             int
	ReadLimit              int64
	WriteTimeout           time.Duration
	SessionRecheckInterval time.Duration
	Logger                 *slog.Logger
	RateLimiter            *ratelimit.Limiter
}

// Server accepts authenticated room subscriptions and fans Valkey events out
// to sockets on this API replica. Each instance owns one environment channel
// subscription regardless of the number of connected sockets.
type Server struct {
	ctx          context.Context
	cancel       context.CancelFunc
	pool         *pgxpool.Pool
	sessionRules auth.SessionRules
	channel      string
	config       config.Config
	options      Options
	logger       *slog.Logger
	pubsub       *redis.PubSub

	mu           sync.RWMutex
	sockets      map[*socket]struct{}
	roomSockets  map[string]map[*socket]struct{}
	closed       bool
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	readerWait   sync.WaitGroup
}

// NewServer subscribes to the environment's Valkey channel before returning,
// so callers can safely start serving WebSocket handshakes afterward.
func NewServer(parent context.Context, pool *pgxpool.Pool, valkey redis.UniversalClient, cfg config.Config, sessionRules auth.SessionRules, options Options) (*Server, error) {
	if parent == nil {
		parent = context.Background()
	}
	if pool == nil || valkey == nil {
		return nil, errors.New("WebSocket server requires PostgreSQL and Valkey")
	}
	if cfg.Environment != config.EnvironmentLocal && options.RateLimiter == nil {
		return nil, errors.New("WebSocket server requires a shared rate limiter outside local mode")
	}
	options = normalizeOptions(options)
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(parent)
	channel := outbox.EventChannel(string(cfg.Environment))
	pubsub := valkey.Subscribe(ctx, channel)
	readyContext, readyCancel := context.WithTimeout(ctx, queryTimeout)
	defer readyCancel()
	if _, err := pubsub.Receive(readyContext); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, errors.New("could not subscribe WebSocket server to Valkey events")
	}
	server := &Server{
		ctx:          ctx,
		cancel:       cancel,
		pool:         pool,
		sessionRules: sessionRules,
		channel:      channel,
		config:       cfg,
		options:      options,
		logger:       logger,
		pubsub:       pubsub,
		sockets:      make(map[*socket]struct{}),
		roomSockets:  make(map[string]map[*socket]struct{}),
		shutdownDone: make(chan struct{}),
	}
	server.readerWait.Add(1)
	go server.readEvents()
	return server, nil
}

func normalizeOptions(options Options) Options {
	if options.QueueFrames <= 0 {
		options.QueueFrames = defaultQueueFrames
	}
	if options.QueueBytes <= 0 {
		options.QueueBytes = defaultQueueBytes
	}
	if options.ReadLimit <= 0 {
		options.ReadLimit = defaultReadLimit
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = defaultWriteTimeout
	}
	if options.SessionRecheckInterval <= 0 {
		options.SessionRecheckInterval = defaultSessionRecheck
	}
	return options
}

// ServeHTTP handles one /api/v1/ws connection. The surrounding HTTP handler
// must have installed the authenticated principal in the request context.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apierror.Write(w, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
		return
	}
	if r.URL.RawQuery != "" {
		apierror.Write(w, http.StatusBadRequest, "INVALID_REQUEST", "Query parameters are not supported", false, nil)
		return
	}
	if !s.originAllowed(r) {
		apierror.Write(w, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "Origin is not allowed", false, nil)
		return
	}
	if !requestsSubprotocol(r, webSocketProtocol) {
		apierror.Write(w, http.StatusBadRequest, "SUBPROTOCOL_REQUIRED", "The chat.v1 subprotocol is required", false, nil)
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		apierror.Write(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return
	}
	valid, err := s.sessionValid(r.Context(), principal)
	if err != nil {
		apierror.Write(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
		return
	}
	if !valid {
		apierror.Write(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return
	}

	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		apierror.Write(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "The WebSocket service is shutting down", true, nil)
		return
	}
	connectionID := requestid.FromContext(r.Context())
	if connectionID == "" {
		generatedID, err := requestid.New()
		if err != nil {
			w.Header().Set("Retry-After", "1")
			apierror.Write(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
			return
		}
		connectionID = generatedID
	}
	acceptOptions := &websocket.AcceptOptions{Subprotocols: []string{webSocketProtocol}}
	if s.config.AppOrigin != "" {
		acceptOptions.OriginPatterns = []string{s.config.AppOrigin}
	}
	connection, err := websocket.Accept(w, r, acceptOptions)
	if err != nil {
		return
	}
	if connection.Subprotocol() != webSocketProtocol {
		_ = connection.Close(websocket.StatusProtocolError, "subprotocol required")
		return
	}
	connection.SetReadLimit(s.options.ReadLimit)
	connectionContext, cancel := context.WithCancel(s.ctx)
	client := &socket{
		server:    s,
		conn:      connection,
		ctx:       connectionContext,
		cancel:    cancel,
		principal: principal,
		requestID: connectionID,
		rooms:     make(map[string]struct{}),
		outbound:  newMessageQueue(s.options.QueueFrames, s.options.QueueBytes),
	}
	if !s.registerSocket(client) {
		cancel()
		_ = connection.Close(websocket.StatusGoingAway, "server is shutting down")
		return
	}
	go client.writeLoop()
	go client.monitorSession()
	client.readLoop()
	client.stop(websocket.StatusNormalClosure, "")
}

// Close drains local sockets with 1001 and stops the shared Valkey reader.
func (s *Server) Close() {
	_ = s.Shutdown(context.Background())
}

// ReportHealth logs the low-cardinality active socket gauge for dashboards.
func (s *Server) ReportHealth() {
	s.mu.RLock()
	active := len(s.sockets)
	s.mu.RUnlock()
	s.logger.Info("websocket connection health", "event", "socket.health", "service", "api", "active_sockets", active)
}

// Shutdown stops accepting sockets, closes active sockets, and waits for the
// Valkey reader under the caller's grace period. Expired grace force-closes
// remaining connections so process exit stays bounded.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		clients := make([]*socket, 0, len(s.sockets))
		for client := range s.sockets {
			clients = append(clients, client)
		}
		s.mu.Unlock()
		go func() {
			_ = s.pubsub.Close()
			var stopWait sync.WaitGroup
			for _, client := range clients {
				stopWait.Add(1)
				go func(client *socket) {
					defer stopWait.Done()
					client.stop(websocket.StatusGoingAway, "")
				}(client)
			}
			stopWait.Wait()
			s.cancel()
			s.readerWait.Wait()
			close(s.shutdownDone)
		}()
	})

	select {
	case <-s.shutdownDone:
		return nil
	case <-ctx.Done():
		s.mu.RLock()
		clients := make([]*socket, 0, len(s.sockets))
		for client := range s.sockets {
			clients = append(clients, client)
		}
		s.mu.RUnlock()
		for _, client := range clients {
			_ = client.conn.CloseNow()
			client.cancel()
		}
		s.cancel()
		return ctx.Err()
	}
}

func (s *Server) readEvents() {
	defer s.readerWait.Done()
	brokerUnavailable := false
	for {
		message, err := s.pubsub.ReceiveMessage(s.ctx)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			if !brokerUnavailable {
				s.logger.Warn("websocket event broker unavailable", "event", "dependency.health", "service", "api",
					"dependency", "valkey_pubsub", "state", "unavailable", "error_code", "REALTIME_PUBSUB_UNAVAILABLE")
				brokerUnavailable = true
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}
		if brokerUnavailable {
			s.logger.Info("websocket event broker recovered", "event", "dependency.health", "service", "api",
				"dependency", "valkey_pubsub", "state", "healthy")
			brokerUnavailable = false
		}
		s.dispatch(message.Payload)
	}
}

func (s *Server) dispatch(payload string) {
	var event chatmessage.CreatedEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil || event.Type != chatmessage.CreatedType || !validMessage(event.Message) {
		return
	}
	frame, err := marshalFrame(outboundFrame{
		roomID:  event.Message.RoomID,
		private: true,
		message: true,
		value:   event,
	})
	if err != nil {
		return
	}
	s.mu.RLock()
	clients := make([]*socket, 0, len(s.roomSockets[frame.roomID]))
	for client := range s.roomSockets[frame.roomID] {
		clients = append(clients, client)
	}
	s.mu.RUnlock()
	for _, client := range clients {
		client.deliver(frame)
	}
}

func marshalFrame(frame outboundFrame) (outboundFrame, error) {
	payload, err := json.Marshal(frame.value)
	if err != nil {
		return outboundFrame{}, err
	}
	frame.payload = payload
	frame.value = nil
	return frame, nil
}

func (s *Server) registerSocket(client *socket) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.sockets[client] = struct{}{}
	active := len(s.sockets)
	s.mu.Unlock()
	s.logger.Info("websocket connected", "event", "socket.connection", "service", "api",
		"request_id", client.requestID, "state", "connected", "active_sockets", active)
	return true
}

func (s *Server) addSubscription(client *socket, roomID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	clients := s.roomSockets[roomID]
	if clients == nil {
		clients = make(map[*socket]struct{})
		s.roomSockets[roomID] = clients
	}
	clients[client] = struct{}{}
	return true
}

func (s *Server) removeSubscription(client *socket, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients := s.roomSockets[roomID]
	delete(clients, client)
	if len(clients) == 0 {
		delete(s.roomSockets, roomID)
	}
}

func (s *Server) removeSocket(client *socket, closeCode websocket.StatusCode) {
	s.mu.Lock()
	_, existed := s.sockets[client]
	delete(s.sockets, client)
	for roomID, clients := range s.roomSockets {
		delete(clients, client)
		if len(clients) == 0 {
			delete(s.roomSockets, roomID)
		}
	}
	active := len(s.sockets)
	s.mu.Unlock()
	if existed {
		s.logger.Info("websocket disconnected", "event", "socket.connection", "service", "api",
			"request_id", client.requestID, "state", "closed", "active_sockets", active, "close_code", int(closeCode))
	}
}

func (s *Server) sessionValid(parent context.Context, principal auth.Principal) (bool, error) {
	if principal.SessionID == "" {
		if s.config.Environment != config.EnvironmentLocal || !s.config.EnableLocalTestIdentity {
			return false, nil
		}
		ctx, cancel := context.WithTimeout(parent, queryTimeout)
		defer cancel()
		var valid bool
		err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM app_users
				WHERE user_id = $1::uuid AND is_active
			)`, principal.UserID).Scan(&valid)
		return valid, err
	}
	return s.sessionRules.Current(parent, principal)
}

func (s *Server) hasMembership(parent context.Context, roomID, userID string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, queryTimeout)
	defer cancel()
	var allowed bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM room_memberships
			WHERE room_id = $1::uuid AND user_id = $2::uuid
		)`, roomID, userID).Scan(&allowed)
	return allowed, err
}

func (s *Server) roomLastSequence(parent context.Context, roomID, userID string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, queryTimeout)
	defer cancel()
	var sequence string
	err := s.pool.QueryRow(ctx, `
		SELECT rooms.last_seq::text
		FROM rooms
		JOIN room_memberships ON room_memberships.room_id = rooms.room_id
		WHERE rooms.room_id = $1::uuid AND room_memberships.user_id = $2::uuid`,
		roomID, userID).Scan(&sequence)
	return sequence, err
}

func sameHostOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	parsed, err := url.Parse(origins[0])
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func (s *Server) originAllowed(r *http.Request) bool {
	if s.config.AppOrigin == "" {
		return sameHostOrigin(r)
	}
	origins := r.Header.Values("Origin")
	return len(origins) == 1 && origins[0] == s.config.AppOrigin
}

func requestsSubprotocol(r *http.Request, wanted string) bool {
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, token := range strings.Split(header, ",") {
			if strings.TrimSpace(token) == wanted {
				return true
			}
		}
	}
	return false
}

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

type messageQueue struct {
	mu        sync.Mutex
	frames    []outboundFrame
	bytes     int
	maxFrames int
	maxBytes  int
	wake      chan struct{}
	closed    bool
}

func newMessageQueue(maxFrames, maxBytes int) *messageQueue {
	return &messageQueue{
		maxFrames: maxFrames,
		maxBytes:  maxBytes,
		wake:      make(chan struct{}, 1),
	}
}

func (q *messageQueue) push(frame outboundFrame) bool {
	q.mu.Lock()
	if q.closed || len(q.frames) >= q.maxFrames || q.bytes+len(frame.payload) > q.maxBytes {
		q.mu.Unlock()
		return false
	}
	q.frames = append(q.frames, frame)
	q.bytes += len(frame.payload)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *messageQueue) pop(ctx context.Context) (outboundFrame, bool) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return outboundFrame{}, false
		}
		if len(q.frames) > 0 {
			frame := q.frames[0]
			q.frames[0] = outboundFrame{}
			q.frames = q.frames[1:]
			q.bytes -= len(frame.payload)
			q.mu.Unlock()
			return frame, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return outboundFrame{}, false
		case <-q.wake:
		}
	}
}

func (q *messageQueue) dropMessageEvents(roomID string) {
	q.mu.Lock()
	filtered := q.frames[:0]
	for _, frame := range q.frames {
		if frame.message && frame.roomID == roomID {
			q.bytes -= len(frame.payload)
			continue
		}
		filtered = append(filtered, frame)
	}
	for index := len(filtered); index < len(q.frames); index++ {
		q.frames[index] = outboundFrame{}
	}
	q.frames = filtered
	q.mu.Unlock()
}

func (q *messageQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.frames = nil
	q.bytes = 0
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

type clientCommand struct {
	Type      string  `json:"type"`
	RequestID *string `json:"request_id"`
	RoomID    string  `json:"room_id,omitempty"`
}

type outboundFrame struct {
	payload []byte
	roomID  string
	private bool
	message bool
	value   any
}

func validMessage(m chatmessage.Message) bool {
	if !uuid.IsCanonical(m.MessageID) || !uuid.IsCanonical(m.RoomID) || !uuid.IsCanonical(m.Sender.UserID) || !uuid.IsCanonical(m.ClientMessageID) || m.Text == "" || m.CreatedAt == "" {
		return false
	}
	sequence, err := strconv.ParseInt(m.Seq, 10, 64)
	return err == nil && sequence > 0 && strconv.FormatInt(sequence, 10) == m.Seq
}

type protocolErrorFrame struct {
	Type      string        `json:"type"`
	RequestID *string       `json:"request_id"`
	Error     protocolError `json:"error"`
}

type protocolError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
