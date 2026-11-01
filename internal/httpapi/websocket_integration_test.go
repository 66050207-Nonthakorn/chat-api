package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	test    *testServer
	pool    *pgxpool.Pool
	valkey  *redis.Client
	primary *httptest.Server
	replica *httptest.Server
}

func newWebSocketTestEnv(t *testing.T, options realtime.Options) *webSocketTestEnv {
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
	primaryHub, err := realtime.NewServer(context.Background(), test.pool, valkey, cfg, options)
	if err != nil {
		t.Fatalf("start primary WebSocket subscriber: %v", err)
	}
	replicaHub, err := realtime.NewServer(context.Background(), test.pool, valkey, cfg, options)
	if err != nil {
		primaryHub.Close()
		t.Fatalf("start second WebSocket subscriber: %v", err)
	}
	primary := httptest.NewServer(httpapi.NewHandlerWithRealtime(cfg, test.pool, nil, primaryHub))
	replica := httptest.NewServer(httpapi.NewHandlerWithRealtime(cfg, test.pool, nil, replicaHub))
	t.Cleanup(func() {
		primary.Close()
		replica.Close()
		primaryHub.Close()
		replicaHub.Close()
	})
	return &webSocketTestEnv{test: test, pool: test.pool, valkey: valkey, primary: primary, replica: replica}
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
