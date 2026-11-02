package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/demo"
	"cloudchat/internal/httpapi"
	"cloudchat/internal/outbox"
	"cloudchat/internal/realtime"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWebSocketHandshakeRequiresIdentityOriginAndSubprotocol(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, test := range []struct {
		name      string
		userID    string
		origin    string
		protocols []string
		status    int
	}{
		{name: "identity", origin: env.replica.URL, protocols: []string{"chat.v1"}, status: http.StatusUnauthorized},
		{name: "origin", userID: demo.UserAID, protocols: []string{"chat.v1"}, status: http.StatusForbidden},
		{name: "foreign origin", userID: demo.UserAID, origin: "https://attacker.example", protocols: []string{"chat.v1"}, status: http.StatusForbidden},
		{name: "subprotocol", userID: demo.UserAID, origin: env.replica.URL, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			connection, response, err := dialWebSocket(ctx, env.replica.URL, test.userID, test.origin, test.protocols)
			if connection != nil {
				_ = connection.Close(websocket.StatusNormalClosure, "")
				t.Fatal("unauthorized handshake unexpectedly upgraded")
			}
			if err == nil || response == nil || response.StatusCode != test.status {
				t.Fatalf("handshake = connection %v, response %v, err %v; want HTTP %d", connection, response, err, test.status)
			}
		})
	}
}

func TestWebSocketRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	for _, payload := range []string{
		`{"type":"ping","request_id":"55555555-5555-4555-8555-555555555555","extra":true}`,
		`{"type":"ping","request_id":"55555555-5555-4555-8555-555555555555"} {}`,
	} {
		t.Run(payload, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client := dialChatSocket(t, ctx, env.replica.URL, demo.UserBID)
			t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
			if err := client.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
				t.Fatalf("write command: %v", err)
			}
			_, _, err := client.Read(ctx)
			if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
				t.Fatalf("close status = %d, error = %v, want policy violation", got, err)
			}
		})
	}
}

func TestWebSocketShutdownClosesClientWithGoingAwayAndRejectsNewConnections(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	cfg := config.Config{
		Environment:             config.EnvironmentLocal,
		DatabaseURL:             os.Getenv("TEST_DATABASE_URL"),
		ValkeyURL:               os.Getenv("TEST_VALKEY_URL"),
		EnableLocalTestIdentity: true,
		CursorHMACKey:           "test-only-room-cursor-signing-key-0123456789",
	}
	sessionRules := auth.NewSessionRules(env.pool)
	hub, err := realtime.NewServer(context.Background(), env.pool, env.valkey, cfg, sessionRules, realtime.Options{})
	if err != nil {
		t.Fatalf("start shutdown-test WebSocket hub: %v", err)
	}
	server := httptest.NewServer(httpapi.NewHandlerWithRealtime(cfg, env.pool, nil, hub))
	t.Cleanup(func() {
		server.Close()
		hub.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := dialChatSocket(t, ctx, server.URL, demo.UserBID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })

	shutdownContext, stopShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopShutdown()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- hub.Shutdown(shutdownContext) }()
	_, _, readErr := client.Read(ctx)
	if got := websocket.CloseStatus(readErr); got != websocket.StatusGoingAway {
		t.Fatalf("WebSocket close status = %d (error %v), want going-away %d", got, readErr, websocket.StatusGoingAway)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("bounded WebSocket shutdown: %v", err)
	}

	connection, response, err := dialWebSocket(ctx, server.URL, demo.UserBID, server.URL, []string{"chat.v1"})
	if connection != nil {
		_ = connection.Close(websocket.StatusNormalClosure, "")
		t.Fatal("new WebSocket connected after shutdown")
	}
	if err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("post-shutdown handshake = response %v, error %v; want HTTP 503", response, err)
	}
}

func TestWebSocketMembershipRevocationStopsPrivateDelivery(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserBID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	requestID := "55555555-5555-4555-8555-555555555555"
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: requestID, RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("subscribe to current membership: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
		t.Fatalf("subscribe response = %s err=%v", got.payload, got.err)
	}

	if _, err := env.pool.Exec(ctx, `DELETE FROM room_memberships WHERE room_id = $1::uuid AND user_id = $2::uuid`, demo.GroupRoomID, demo.UserBID); err != nil {
		t.Fatalf("revoke room membership: %v", err)
	}
	created := postWebSocketMessage(t, env.primary, demo.GroupRoomID, "must not reach former member")
	worker := outbox.NewWorker(env.pool, env.valkey, config.EnvironmentLocal)
	result, err := worker.ProcessBatch(ctx)
	if err != nil || result.Published != 1 {
		t.Fatalf("publish after access revocation: result=%+v err=%v", result, err)
	}
	got := readSocketFrame(ctx, client)
	var revoked struct {
		Type   string `json:"type"`
		RoomID string `json:"room_id"`
		Reason string `json:"reason"`
	}
	if got.err != nil || json.Unmarshal(got.payload, &revoked) != nil || revoked.Type != "subscription.revoked" || revoked.RoomID != demo.GroupRoomID || revoked.Reason != "ACCESS_REVOKED" {
		t.Fatalf("revocation response = %s err=%v; message %s must not be sent", got.payload, got.err, created.Message.MessageID)
	}
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: "66666666-6666-4666-8666-666666666666", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("send subscribe after revocation: %v", err)
	}
	denied := readSocketFrame(ctx, client)
	var deniedFrame struct {
		Type  string `json:"type"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if denied.err != nil || json.Unmarshal(denied.payload, &deniedFrame) != nil || deniedFrame.Type != "error" || deniedFrame.Error.Code != "ROOM_NOT_FOUND" {
		t.Fatalf("subscribe after removal = %s err=%v, want ROOM_NOT_FOUND", denied.payload, denied.err)
	}
}

func TestWebSocketSessionRevocationClosesIdleSocket(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{SessionRecheckInterval: 40 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionID := "66666666-6666-4666-8666-666666666666"
	createWebSocketTestSession(t, env.pool, demo.UserBID, sessionID)
	server := newSessionSocketServer(t, env, demo.UserBID, sessionID, realtime.Options{SessionRecheckInterval: 40 * time.Millisecond})
	client := dialChatSocket(t, ctx, server.URL, "")
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: "55555555-5555-4555-8555-555555555555", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("subscribe with valid session: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
		t.Fatalf("subscribe response = %s err=%v", got.payload, got.err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE app_sessions SET revoked_at = clock_timestamp() WHERE session_id = $1::uuid`, sessionID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	_, _, err := client.Read(ctx)
	if got := websocket.CloseStatus(err); got != 4401 {
		t.Fatalf("close status after idle session revocation = %d err=%v, want 4401", got, err)
	}
}

func TestWebSocketSessionRevocationBlocksNextPrivateEvent(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sessionID := "99999999-9999-4999-8999-999999999999"
	createWebSocketTestSession(t, env.pool, demo.UserBID, sessionID)
	server := newSessionSocketServer(t, env, demo.UserBID, sessionID, realtime.Options{SessionRecheckInterval: time.Hour})
	client := dialChatSocket(t, ctx, server.URL, "")
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: "55555555-5555-4555-8555-555555555555", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("subscribe with valid session: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
		t.Fatalf("subscribe response = %s err=%v", got.payload, got.err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE app_sessions SET revoked_at = clock_timestamp() WHERE session_id = $1::uuid`, sessionID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	_ = postWebSocketMessage(t, env.primary, demo.GroupRoomID, "must not reach revoked session")
	worker := outbox.NewWorker(env.pool, env.valkey, config.EnvironmentLocal)
	result, err := worker.ProcessBatch(ctx)
	if err != nil || result.Published != 1 {
		t.Fatalf("publish after session revocation: result=%+v err=%v", result, err)
	}
	_, _, err = client.Read(ctx)
	if got := websocket.CloseStatus(err); got != 4401 {
		t.Fatalf("close status before private event after session revocation = %d err=%v, want 4401", got, err)
	}
}

func TestWebSocketPingUnsubscribeAndHTTPSyncRecoverMissedEvents(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserAID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	requestID := "55555555-5555-4555-8555-555555555555"
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: requestID, RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
		t.Fatalf("subscribe response = %s err=%v", got.payload, got.err)
	}
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: "66666666-6666-4666-8666-666666666666", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("repeat subscribe: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
		t.Fatalf("repeat subscribe response = %s err=%v", got.payload, got.err)
	}
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "ping", RequestID: "77777777-7777-4777-8777-777777777777"}); err != nil {
		t.Fatalf("send application ping: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"pong"`) {
		t.Fatalf("ping response = %s err=%v", got.payload, got.err)
	}
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "unsubscribe", RequestID: "88888888-8888-4888-8888-888888888888", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"unsubscribed"`) {
		t.Fatalf("unsubscribe response = %s err=%v", got.payload, got.err)
	}

	first := postWebSocketMessage(t, env.primary, demo.GroupRoomID, "published while unsubscribed")
	worker := outbox.NewWorker(env.pool, env.valkey, config.EnvironmentLocal)
	result, err := worker.ProcessBatch(ctx)
	if err != nil || result.Published != 1 {
		t.Fatalf("publish while unsubscribed: result=%+v err=%v", result, err)
	}
	assertNoSocketFrame(t, client, 150*time.Millisecond)
	_ = client.Close(websocket.StatusNormalClosure, "")

	second := postWebSocketMessage(t, env.primary, demo.GroupRoomID, "published while disconnected")
	result, err = worker.ProcessBatch(ctx)
	if err != nil || result.Published != 1 {
		t.Fatalf("publish while disconnected: result=%+v err=%v", result, err)
	}
	reconnected := dialChatSocket(t, ctx, env.replica.URL, demo.UserAID)
	t.Cleanup(func() { _ = reconnected.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, reconnected, websocketCommand{Type: "subscribe", RequestID: "99999999-9999-4999-8999-999999999999", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("resubscribe after reconnect: %v", err)
	}
	if got := readSocketFrame(ctx, reconnected); got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
		t.Fatalf("reconnect subscribe response = %s err=%v", got.payload, got.err)
	}
	assertNoSocketFrame(t, reconnected, 150*time.Millisecond)

	response, body := env.request(t, env.primary, http.MethodGet,
		"/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=0&through_seq="+second.Message.Seq, demo.UserAID, "")
	var synced syncResponseBody
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &synced) != nil || len(synced.Items) != 2 {
		t.Fatalf("HTTP sync after missed live notifications status=%d body=%s", response.StatusCode, body)
	}
	if synced.Items[0].MessageID != first.Message.MessageID || synced.Items[1].MessageID != second.Message.MessageID {
		t.Fatalf("sync order = %+v, want durable messages %s then %s", synced.Items, first.Message.MessageID, second.Message.MessageID)
	}
}

func TestWebSocketClosesWhenOutboundQueueExceedsByteLimit(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{QueueBytes: 300})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserAID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: "55555555-5555-4555-8555-555555555555", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if got := readSocketFrame(ctx, client); got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
		t.Fatalf("subscribe response = %s err=%v", got.payload, got.err)
	}
	_ = postWebSocketMessage(t, env.primary, demo.GroupRoomID, strings.Repeat("x", 4000))
	worker := outbox.NewWorker(env.pool, env.valkey, config.EnvironmentLocal)
	result, err := worker.ProcessBatch(ctx)
	if err != nil || result.Published != 1 {
		t.Fatalf("publish message to full socket queue: result=%+v err=%v", result, err)
	}
	_, _, err = client.Read(ctx)
	if got := websocket.CloseStatus(err); got != 1013 {
		t.Fatalf("close status on outbound queue overflow = %d err=%v, want 1013", got, err)
	}
}

func TestWebSocketRejectsOversizedClientFrameWithClose1009(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserAID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	if err := client.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 20<<10))); err != nil {
		t.Fatalf("send oversized frame: %v", err)
	}
	_, _, err := client.Read(ctx)
	if got := websocket.CloseStatus(err); got != 1009 {
		t.Fatalf("close status on oversized frame = %d err=%v, want 1009", got, err)
	}
}

func TestWebSocketLimitsSubscriptionsPerSocket(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rooms := make([]string, maxTestSubscriptions)
	tx, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin room fixture transaction: %v", err)
	}
	for index := range rooms {
		roomID := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", index+1)
		rooms[index] = roomID
		if _, err := tx.Exec(ctx, `INSERT INTO rooms (room_id, kind, title, owner_user_id) VALUES ($1::uuid, 'group', $2, $3::uuid)`, roomID, fmt.Sprintf("test room %d", index+1), demo.UserAID); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("insert test room %s: %v", roomID, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO room_memberships (room_id, user_id, member_role) VALUES ($1::uuid, $2::uuid, 'member')`, roomID, demo.UserAID); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("insert membership for %s: %v", roomID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit test rooms: %v", err)
	}
	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserAID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	for index, roomID := range rooms {
		requestID := fmt.Sprintf("bbbbbbbb-bbbb-4bbb-8bbb-%012d", index+1)
		if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: requestID, RoomID: roomID}); err != nil {
			t.Fatalf("subscribe to room %d: %v", index+1, err)
		}
		got := readSocketFrame(ctx, client)
		if got.err != nil || !strings.Contains(string(got.payload), `"type":"subscribed"`) {
			t.Fatalf("subscription %d response = %s err=%v", index+1, got.payload, got.err)
		}
	}
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: "cccccccc-cccc-4ccc-8ccc-000000000001", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("send over-limit subscribe: %v", err)
	}
	got := readSocketFrame(ctx, client)
	var response struct {
		Type  string `json:"type"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if got.err != nil || json.Unmarshal(got.payload, &response) != nil || response.Type != "error" || response.Error.Code != "SUBSCRIPTION_LIMIT" {
		t.Fatalf("over-limit subscribe response = %s err=%v, want SUBSCRIPTION_LIMIT", got.payload, got.err)
	}
}

const maxTestSubscriptions = 50

func assertNoSocketFrame(t *testing.T, client *websocket.Conn, duration time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration+2*time.Second)
	defer cancel()
	result := make(chan socketRead, 1)
	go func() { result <- readSocketFrame(ctx, client) }()
	select {
	case got := <-result:
		t.Fatalf("unexpected frame after unsubscribe/reconnect: payload=%s err=%v", got.payload, got.err)
	case <-time.After(duration):
		_ = client.Close(websocket.StatusNormalClosure, "")
	}
}

func newSessionSocketServer(t *testing.T, env *webSocketTestEnv, userID, sessionID string, options realtime.Options) *httptest.Server {
	t.Helper()
	cfg := config.Config{
		Environment:             config.EnvironmentLocal,
		ValkeyURL:               os.Getenv("TEST_VALKEY_URL"),
		EnableLocalTestIdentity: true,
		CursorHMACKey:           "test-only-room-cursor-signing-key-0123456789",
	}
	sessionRules := auth.NewSessionRules(env.pool)
	hub, err := realtime.NewServer(context.Background(), env.pool, env.valkey, cfg, sessionRules, options)
	if err != nil {
		t.Fatalf("start session test WebSocket subscriber: %v", err)
	}
	handler := httpapi.NewHandlerWithRealtime(cfg, env.pool, nil, hub)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := auth.Principal{UserID: userID, DisplayName: "Demo B", SessionID: sessionID}
		handler.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
	}))
	t.Cleanup(func() {
		server.Close()
		hub.Close()
	})
	return server
}

func createWebSocketTestSession(t *testing.T, pool *pgxpool.Pool, userID, sessionID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	identityID := "77777777-7777-4777-8777-777777777777"
	tokenHash := sha256.Sum256([]byte("test-token:" + sessionID))
	csrfHash := sha256.Sum256([]byte("test-csrf:" + sessionID))
	expiresAt := time.Now().UTC().Add(time.Hour)
	idleExpiresAt := time.Now().UTC().Add(30 * time.Minute)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_identities (identity_id, user_id, issuer, subject)
		VALUES ($1::uuid, $2::uuid, 'test-issuer', $3)
		`, identityID, userID, "subject:"+sessionID); err != nil {
		t.Fatalf("insert test identity: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO app_sessions (
			session_id, token_hash, csrf_token_hash, user_id, identity_id,
			auth_epoch, tokens_ciphertext, encryption_key_id, token_expires_at,
			expires_at, idle_expires_at
		) VALUES ($1::uuid, $2, $3, $4::uuid, $5::uuid, 0, $6, 'test-key', $7, $7, $8)
		`, sessionID, tokenHash[:], csrfHash[:], userID, identityID, []byte("encrypted"), expiresAt, idleExpiresAt); err != nil {
		t.Fatalf("insert test session: %v", err)
	}
}
