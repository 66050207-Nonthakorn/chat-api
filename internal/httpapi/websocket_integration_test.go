package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/demo"
	"cloudchat/internal/httpapi"
	"cloudchat/internal/outbox"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/realtime"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestWebSocketSubscribeAcknowledgesBeforeCrossReplicaDelivery(t *testing.T) {
	env := newWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first := postWebSocketMessage(t, env.primary, demo.GroupRoomID, "first live event")
	lock, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin subscribe barrier: %v", err)
	}
	if _, err := lock.Exec(ctx, "LOCK TABLE rooms IN ACCESS EXCLUSIVE MODE"); err != nil {
		_ = lock.Rollback(context.Background())
		t.Fatalf("block room snapshot query: %v", err)
	}

	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserBID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, client, websocketCommand{
		Type: "subscribe", RequestID: "55555555-5555-4555-8555-555555555555", RoomID: demo.GroupRoomID,
	}); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}

	firstRead := make(chan socketRead, 1)
	go func() {
		firstRead <- readSocketFrame(ctx, client)
	}()
	select {
	case result := <-firstRead:
		if result.err != nil {
			t.Fatalf("read before room snapshot was released: %v", result.err)
		}
		t.Fatalf("received frame before room snapshot lock released: %s", result.payload)
	case <-time.After(150 * time.Millisecond):
	}

	worker := outbox.NewWorker(env.test.pool, env.valkey, config.EnvironmentLocal)
	published, err := worker.ProcessBatch(ctx)
	if err != nil || published.Published != 1 {
		t.Fatalf("publish first committed message: result=%+v err=%v", published, err)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("release room snapshot lock: %v", err)
	}

	ack := <-firstRead
	if ack.err != nil {
		t.Fatalf("read subscribe acknowledgement: %v", ack.err)
	}
	var subscribed struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		RoomID    string `json:"room_id"`
		LastSeq   string `json:"last_seq"`
	}
	if err := json.Unmarshal(ack.payload, &subscribed); err != nil {
		t.Fatalf("decode subscribe acknowledgement %s: %v", ack.payload, err)
	}
	if subscribed.Type != "subscribed" || subscribed.RequestID != "55555555-5555-4555-8555-555555555555" || subscribed.RoomID != demo.GroupRoomID || subscribed.LastSeq != first.Message.Seq {
		t.Fatalf("subscribe acknowledgement = %+v, want snapshot sequence %s", subscribed, first.Message.Seq)
	}

	second := postWebSocketMessage(t, env.primary, demo.GroupRoomID, "cross replica")
	published, err = worker.ProcessBatch(ctx)
	if err != nil || published.Published != 1 {
		t.Fatalf("publish second committed message: result=%+v err=%v", published, err)
	}
	firstEvent := readSocketFrame(ctx, client)
	secondEvent := readSocketFrame(ctx, client)
	assertMessageEvent(t, firstEvent, first.Message)
	assertMessageEvent(t, secondEvent, second.Message)

	publishTestMessage(t, env, second.Message)
	publishTestMessage(t, env, first.Message)
	duplicateSecond := readSocketFrame(ctx, client)
	duplicateFirst := readSocketFrame(ctx, client)
	assertMessageEvent(t, duplicateSecond, second.Message)
	assertMessageEvent(t, duplicateFirst, first.Message)

	var synced syncResponseBody
	response, body := env.request(t, env.primary, http.MethodGet,
		"/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=0&through_seq="+second.Message.Seq, demo.UserBID, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sync after live delivery status = %d, body = %s", response.StatusCode, body)
	}
	if err := json.Unmarshal(body, &synced); err != nil {
		t.Fatalf("decode recovery sync response %s: %v", body, err)
	}
	if len(synced.Items) != 2 || synced.Items[0].MessageID != first.Message.MessageID || synced.Items[1].MessageID != second.Message.MessageID {
		t.Fatalf("HTTP sync = %+v, want both committed messages in sequence", synced)
	}
}

func TestProductionRealtimeServerRequiresSharedRateLimiter(t *testing.T) {
	env := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	productionConfig := config.Config{Environment: config.EnvironmentProduction}
	sessionRules := auth.NewSessionRules(env.pool)
	server, err := realtime.NewServer(context.Background(), env.pool, env.valkey, productionConfig, sessionRules, realtime.Options{})
	if err == nil {
		server.Close()
		t.Fatal("production realtime server started without a shared rate limiter")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "rate limiter") {
		t.Fatalf("server error=%q, want missing rate-limiter configuration", err)
	}
}

func TestWebSocketSubscribeAcknowledgementLogsLatencyAndSafeCorrelation(t *testing.T) {
	logs := &synchronizedLogBuffer{}
	env := newWebSocketTestEnv(t, realtime.Options{Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserBID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	const clientRequestID = "55555555-5555-4555-8555-555555555555"
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: clientRequestID, RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}
	if ack := readSocketFrame(ctx, client); ack.err != nil || !strings.Contains(string(ack.payload), `"type":"subscribed"`) {
		t.Fatalf("subscribe acknowledgement = %s err=%v", ack.payload, ack.err)
	}
	const rejectedRequestID = "66666666-6666-4666-8666-666666666666"
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: rejectedRequestID, RoomID: "invalid-room-id"}); err != nil {
		t.Fatalf("send rejected subscribe: %v", err)
	}
	if rejected := readSocketFrame(ctx, client); rejected.err != nil || !strings.Contains(string(rejected.payload), `"type":"error"`) {
		t.Fatalf("rejected subscribe acknowledgement = %s err=%v", rejected.payload, rejected.err)
	}

	foundSent, foundError := false, false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (!foundSent || !foundError) {
		foundSent, foundError = false, false
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("decode WebSocket log %q: %v", line, err)
			}
			if event["event"] != "socket.ack" {
				continue
			}
			if _, ok := event["enqueue_duration_ms"].(float64); !ok {
				t.Fatalf("acknowledgement has no numeric latency: %+v", event)
			}
			if event["request_id"] == "" || event["request_id"] == clientRequestID || event["request_id"] == rejectedRequestID || event["room_id"] != nil {
				t.Fatalf("acknowledgement log has unsafe correlation or room data: %+v", event)
			}
			switch {
			case event["ack_type"] == "subscribe" && event["state"] == "queued":
				foundSent = true
			case event["ack_type"] == "error" && event["state"] == "error":
				foundError = true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !foundSent || !foundError {
		t.Fatalf("missing socket acknowledgement metric (sent=%t error=%t): %s", foundSent, foundError, logs.String())
	}
}

func TestWebSocketBrokerReconnectLogsUnavailableDependency(t *testing.T) {
	logs := &synchronizedLogBuffer{}
	env := newWebSocketTestEnv(t, realtime.Options{Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := dialChatSocket(t, ctx, env.replica.URL, demo.UserBID)
	t.Cleanup(func() { _ = client.Close(websocket.StatusNormalClosure, "") })
	if err := wsjson.Write(ctx, client, websocketCommand{Type: "subscribe", RequestID: "55555555-5555-4555-8555-555555555555", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("subscribe over public WebSocket: %v", err)
	}
	if ack := readSocketFrame(ctx, client); ack.err != nil || !strings.Contains(string(ack.payload), `"type":"subscribed"`) {
		t.Fatalf("subscribe acknowledgement = %s err=%v", ack.payload, ack.err)
	}

	clientList, err := env.valkey.Do(ctx, "CLIENT", "LIST").Text()
	if err != nil {
		t.Fatalf("list Valkey connections: %v", err)
	}
	var pubsubID string
	for _, line := range strings.Split(clientList, "\n") {
		var id string
		isTestClient, isSubscriber := false, false
		for _, field := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(field, "id="):
				id = strings.TrimPrefix(field, "id=")
			case field == "name="+env.valkey.Options().ClientName:
				isTestClient = true
			case field == "cmd=subscribe":
				isSubscriber = true
			}
		}
		if isTestClient && isSubscriber {
			pubsubID = id
			break
		}
	}
	if pubsubID == "" {
		t.Fatalf("could not find this test's Pub/Sub connection in CLIENT LIST: %s", clientList)
	}
	if killed, err := env.valkey.Do(ctx, "CLIENT", "KILL", "ID", pubsubID).Int(); err != nil || killed != 1 {
		t.Fatalf("kill only this test's Pub/Sub connection: killed=%d err=%v", killed, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var event map[string]any
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("decode broker health log %q: %v", line, err)
			}
			if event["event"] == "dependency.health" && event["dependency"] == "valkey_pubsub" && event["state"] == "unavailable" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Valkey Pub/Sub interruption produced no broker failure event: %s", logs.String())
}

type synchronizedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *synchronizedLogBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *synchronizedLogBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func assertMessageEvent(t *testing.T, got socketRead, want messageResponse) {
	t.Helper()
	var delivered struct {
		Type    string `json:"type"`
		Message struct {
			MessageID string `json:"message_id"`
			RoomID    string `json:"room_id"`
			Seq       string `json:"seq"`
			Text      string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(got.payload, &delivered); err != nil {
		t.Fatalf("decode message event %s: %v", got.payload, err)
	}
	if got.err != nil || delivered.Type != "message.created" || delivered.Message.MessageID != want.MessageID || delivered.Message.RoomID != want.RoomID || delivered.Message.Seq != want.Seq || delivered.Message.Text != want.Text {
		t.Fatalf("delivered event = %+v err=%v, want committed message %+v", delivered, got.err, want)
	}
}

func publishTestMessage(t *testing.T, env *webSocketTestEnv, message messageResponse) {
	t.Helper()
	payload, err := json.Marshal(struct {
		Type    string          `json:"type"`
		Message messageResponse `json:"message"`
	}{Type: "message.created", Message: message})
	if err != nil {
		t.Fatalf("encode duplicate message event: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := env.valkey.Publish(ctx, outbox.EventChannel(string(config.EnvironmentLocal)), string(payload)).Err(); err != nil {
		t.Fatalf("publish duplicate/out-of-order message event: %v", err)
	}
}

type webSocketTestEnv struct {
	test                   *testServer
	pool                   *pgxpool.Pool
	valkey                 *redis.Client
	primary                *httptest.Server
	replica                *httptest.Server
	primaryRateLimitClient *redis.Client
	replicaRateLimitClient *redis.Client
}

func newWebSocketTestEnv(t *testing.T, options realtime.Options) *webSocketTestEnv {
	return newWebSocketTestEnvWithRateLimit(t, options, false)
}

func newRateLimitedWebSocketTestEnv(t *testing.T, options realtime.Options) *webSocketTestEnv {
	return newWebSocketTestEnvWithRateLimit(t, options, true)
}

func newWebSocketTestEnvWithRateLimit(t *testing.T, options realtime.Options, enabled bool) *webSocketTestEnv {
	t.Helper()
	valkeyURL := strings.TrimSpace(os.Getenv("TEST_VALKEY_URL"))
	if valkeyURL == "" {
		t.Skip("set TEST_VALKEY_URL to run WebSocket integration tests")
	}
	test := newTestServer(t)
	redisOptions, err := redis.ParseURL(valkeyURL)
	if err != nil {
		t.Fatalf("parse test Valkey URL: %v", err)
	}
	redisOptions.ClientName = fmt.Sprintf("chat-ws-test-%d", time.Now().UnixNano())
	valkey := redis.NewClient(redisOptions)
	t.Cleanup(func() { _ = valkey.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := valkey.Ping(ctx).Err(); err != nil {
		t.Fatalf("connect to test Valkey: %v", err)
	}

	cfg := config.Config{
		Environment:             config.EnvironmentLocal,
		DatabaseURL:             os.Getenv("TEST_DATABASE_URL"),
		ValkeyURL:               valkeyURL,
		EnableLocalTestIdentity: true,
		CursorHMACKey:           "test-only-room-cursor-signing-key-0123456789",
	}
	var primaryRateLimitClient, replicaRateLimitClient *redis.Client
	var primaryLimiter, replicaLimiter *ratelimit.Limiter
	if enabled {
		namespace := fmt.Sprintf("local-test-%d", time.Now().UnixNano())
		primaryOptions := *redisOptions
		primaryOptions.ClientName = fmt.Sprintf("chat-rate-test-primary-%d", time.Now().UnixNano())
		replicaOptions := *redisOptions
		replicaOptions.ClientName = fmt.Sprintf("chat-rate-test-replica-%d", time.Now().UnixNano())
		primaryRateLimitClient = redis.NewClient(&primaryOptions)
		replicaRateLimitClient = redis.NewClient(&replicaOptions)
		t.Cleanup(func() { _ = primaryRateLimitClient.Close() })
		t.Cleanup(func() { _ = replicaRateLimitClient.Close() })
		primaryLimiter, err = ratelimit.New(primaryRateLimitClient, namespace)
		if err != nil {
			t.Fatalf("create primary Valkey rate limiter: %v", err)
		}
		replicaLimiter, err = ratelimit.New(replicaRateLimitClient, namespace)
		if err != nil {
			t.Fatalf("create replica Valkey rate limiter: %v", err)
		}
	}
	primaryServerOptions := options
	primaryServerOptions.RateLimiter = primaryLimiter
	replicaServerOptions := options
	replicaServerOptions.RateLimiter = replicaLimiter
	sessionRules := auth.NewSessionRules(test.pool)
	primaryHub, err := realtime.NewServer(context.Background(), test.pool, valkey, cfg, sessionRules, primaryServerOptions)
	if err != nil {
		t.Fatalf("start primary WebSocket subscriber: %v", err)
	}
	replicaHub, err := realtime.NewServer(context.Background(), test.pool, valkey, cfg, sessionRules, replicaServerOptions)
	if err != nil {
		primaryHub.Close()
		t.Fatalf("start second WebSocket subscriber: %v", err)
	}
	primaryHandler := httpapi.NewHandlerWithRealtime(cfg, test.pool, nil, primaryHub)
	replicaHandler := httpapi.NewHandlerWithRealtime(cfg, test.pool, nil, replicaHub)
	if enabled {
		primaryHandler = httpapi.NewHandlerWithAuthAndRateLimiter(cfg, test.pool, nil, primaryHub, nil, primaryLimiter)
		replicaHandler = httpapi.NewHandlerWithAuthAndRateLimiter(cfg, test.pool, nil, replicaHub, nil, replicaLimiter)
	}
	primary := httptest.NewServer(primaryHandler)
	replica := httptest.NewServer(replicaHandler)
	t.Cleanup(func() {
		primary.Close()
		replica.Close()
		primaryHub.Close()
		replicaHub.Close()
	})
	return &webSocketTestEnv{
		test: test, pool: test.pool, valkey: valkey, primary: primary, replica: replica,
		primaryRateLimitClient: primaryRateLimitClient, replicaRateLimitClient: replicaRateLimitClient,
	}
}

func (env *webSocketTestEnv) request(t *testing.T, server *httptest.Server, method, path, userID, body string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create HTTP request: %v", err)
	}
	request.Header.Set(auth.LocalTestUserHeader, userID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("send HTTP request: %v", err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	return response, result
}

func postWebSocketMessage(t *testing.T, server *httptest.Server, roomID, text string) sendMessageResponse {
	t.Helper()
	clientMessageID := fmt.Sprintf("%08x-5555-4555-8555-%012x", time.Now().UnixNano()&0xffffffff, time.Now().UnixNano()&0xffffffffffff)
	body := fmt.Sprintf(`{"client_message_id":%q,"text":%q}`, clientMessageID, text)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/rooms/"+roomID+"/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create send-message request: %v", err)
	}
	request.Header.Set(auth.LocalTestUserHeader, demo.UserAID)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("send committed message: %v", err)
	}
	defer response.Body.Close()
	var created sendMessageResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode send-message response: %v", err)
	}
	if response.StatusCode != http.StatusCreated || created.Replayed {
		t.Fatalf("send-message status=%d result=%+v", response.StatusCode, created)
	}
	return created
}

func dialChatSocket(t *testing.T, ctx context.Context, baseURL, userID string) *websocket.Conn {
	t.Helper()
	connection, response, err := dialWebSocket(ctx, baseURL, userID, baseURL, []string{"chat.v1"})
	if err != nil {
		if response != nil {
			t.Fatalf("dial WebSocket: %v (HTTP %d)", err, response.StatusCode)
		}
		t.Fatalf("dial WebSocket: %v", err)
	}
	if connection.Subprotocol() != "chat.v1" {
		t.Fatalf("negotiated subprotocol = %q, want chat.v1", connection.Subprotocol())
	}
	return connection
}

func dialWebSocket(ctx context.Context, baseURL, userID, origin string, protocols []string) (*websocket.Conn, *http.Response, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, nil, err
	}
	parsed.Scheme = "ws"
	parsed.Path = "/api/v1/ws"
	header := make(http.Header)
	if userID != "" {
		header.Set(auth.LocalTestUserHeader, userID)
	}
	if origin != "" {
		header.Set("Origin", origin)
	}
	connection, response, err := websocket.Dial(ctx, parsed.String(), &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: protocols,
	})
	return connection, response, err
}

type websocketCommand struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RoomID    string `json:"room_id,omitempty"`
}

type socketRead struct {
	payload []byte
	err     error
}

func readSocketFrame(ctx context.Context, connection *websocket.Conn) socketRead {
	_, payload, err := connection.Read(ctx)
	return socketRead{payload: payload, err: err}
}
