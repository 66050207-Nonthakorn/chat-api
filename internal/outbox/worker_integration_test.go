package outbox_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudchat/internal/config"
	"cloudchat/internal/demo"
	"cloudchat/internal/httpapi"
	"cloudchat/internal/migrations"
	"cloudchat/internal/outbox"
	"cloudchat/internal/seed"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const testClientMessageID = "55555555-5555-4555-8555-555555555555"

func TestPublishesCommittedMessageToValkeyAndMarksOutbox(t *testing.T) {
	env := newWorkerTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel := outbox.EventChannel(string(config.EnvironmentLocal))
	subscriber := env.subscribe(t, ctx, channel)
	created := createMessage(t, env.api.Client(), env.api.URL)
	worker := outbox.NewWorker(env.pool, env.valkey, config.EnvironmentLocal)
	result, err := worker.ProcessBatch(ctx)
	if err != nil {
		t.Fatalf("process outbox batch: %v", err)
	}
	if result.Published != 1 {
		t.Fatalf("published count = %d, want 1", result.Published)
	}

	messageCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	pubsubMessage, err := subscriber.ReceiveMessage(messageCtx)
	if err != nil {
		t.Fatalf("receive published event: %v", err)
	}
	var event struct {
		Type    string `json:"type"`
		Message struct {
			MessageID string `json:"message_id"`
			RoomID    string `json:"room_id"`
			Seq       string `json:"seq"`
			Sender    struct {
				UserID      string `json:"user_id"`
				DisplayName string `json:"display_name"`
			} `json:"sender"`
			ClientMessageID string `json:"client_message_id"`
			Text            string `json:"text"`
			CreatedAt       string `json:"created_at"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(pubsubMessage.Payload), &event); err != nil {
		t.Fatalf("decode published event %q: %v", pubsubMessage.Payload, err)
	}
	if event.Type != "message.created" ||
		event.Message.MessageID != created.Message.MessageID ||
		event.Message.RoomID != created.Message.RoomID ||
		event.Message.Seq != created.Message.Seq ||
		event.Message.Sender.UserID != created.Message.Sender.UserID ||
		event.Message.Sender.DisplayName != created.Message.Sender.DisplayName ||
		event.Message.ClientMessageID != testClientMessageID ||
		event.Message.Text != created.Message.Text ||
		event.Message.CreatedAt != created.Message.CreatedAt {
		t.Fatalf("published event = %+v, want message.created for HTTP message %+v", event, created.Message)
	}
	if pubsubMessage.Channel != channel {
		t.Errorf("published channel = %q, want environment channel %q", pubsubMessage.Channel, channel)
	}

	synced := syncMessage(t, env.api.Client(), env.api.URL, created.Message.RoomID, created.Message.Seq)
	if synced.MessageID != created.Message.MessageID || synced.Text != created.Message.Text {
		t.Fatalf("HTTP sync message = %+v, want committed message %+v", synced, created.Message)
	}
}

func TestRecoversAnExpiredLeaseAfterWorkerStopsBeforePublish(t *testing.T) {
	env := newWorkerTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel := outbox.EventChannel(string(config.EnvironmentLocal))
	subscriber := env.subscribe(t, ctx, channel)
	created := createMessage(t, env.api.Client(), env.api.URL)

	// Simulate a worker process exiting after a claim but before publishing.
	_, err := env.pool.Exec(ctx, `
		UPDATE message_outbox
		SET lease_token = $2::uuid, lease_until = clock_timestamp() - interval '1 second'
		WHERE message_id = $1::uuid`, created.Message.MessageID, "66666666-6666-4666-8666-666666666666")
	if err != nil {
		t.Fatalf("inject an expired lease: %v", err)
	}

	worker := outbox.NewWorker(env.pool, env.valkey, config.EnvironmentLocal)
	result, err := worker.ProcessBatch(ctx)
	if err != nil {
		t.Fatalf("recover expired outbox lease: %v", err)
	}
	if result.Claimed != 1 || result.Published != 1 {
		t.Fatalf("recovery result = %+v, want one claimed and published item", result)
	}
	assertEventMatchesMessage(t, receiveEvent(t, subscriber), created.Message)
	if synced := syncMessage(t, env.api.Client(), env.api.URL, created.Message.RoomID, created.Message.Seq); synced.MessageID != created.Message.MessageID {
		t.Fatalf("HTTP sync returned %+v, want the committed message", synced)
	}
}

func TestShutdownReleasesClaimedOutboxWorkForAnImmediateReplacementWorker(t *testing.T) {
	env := newWorkerTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subscriber := env.subscribe(t, ctx, outbox.EventChannel(string(config.EnvironmentLocal)))
	created := createMessage(t, env.api.Client(), env.api.URL)

	gate := &gatedPublisher{
		UniversalClient: env.valkey,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	worker := outbox.NewWorkerWithOptions(env.pool, gate, config.EnvironmentLocal, outbox.WorkerOptions{
		BatchSize:     1,
		Concurrency:   1,
		ShutdownGrace: 2 * time.Second,
	})
	workerDone := make(chan error, 1)
	workerCtx, stopWorker := context.WithCancel(ctx)
	go func() { workerDone <- worker.Run(workerCtx) }()

	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("worker did not begin publishing the committed outbox event")
	}
	stopWorker()
	select {
	case err := <-workerDone:
		if err != nil {
			t.Fatalf("graceful worker shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not finish within its shutdown grace")
	}

	replacement := outbox.NewWorkerWithOptions(env.pool, env.valkey, config.EnvironmentLocal, outbox.WorkerOptions{
		BatchSize: 1,
	})
	result, err := replacement.ProcessBatch(ctx)
	if err != nil {
		t.Fatalf("replacement worker process released event: %v", err)
	}
	if result.Claimed != 1 || result.Published != 1 {
		t.Fatalf("replacement worker result = %+v, want one immediate publish", result)
	}
	assertEventMatchesMessage(t, receiveEvent(t, subscriber), created.Message)
}

func TestStalePublisherCannotFinishAfterAReplacementOwnsTheLease(t *testing.T) {
	env := newWorkerTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subscriber := env.subscribe(t, ctx, outbox.EventChannel(string(config.EnvironmentLocal)))
	created := createMessage(t, env.api.Client(), env.api.URL)

	gate := &gatedPublisher{
		UniversalClient: env.valkey,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseStale := func() { releaseOnce.Do(func() { close(gate.release) }) }
	t.Cleanup(releaseStale)
	staleWorker := outbox.NewWorkerWithOptions(env.pool, gate, config.EnvironmentLocal, outbox.WorkerOptions{
		BatchSize:      1,
		Concurrency:    1,
		LeaseDuration:  250 * time.Millisecond,
		PublishTimeout: 3 * time.Second,
	})
	staleResult := make(chan struct {
		result outbox.BatchResult
		err    error
	}, 1)
	go func() {
		result, err := staleWorker.ProcessBatch(ctx)
		staleResult <- struct {
			result outbox.BatchResult
			err    error
		}{result, err}
	}()
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		releaseStale()
		t.Fatal("stale worker did not reach Valkey publish")
	}

	replacement := outbox.NewWorker(env.pool, env.valkey, config.EnvironmentLocal)
	whileLeased, err := replacement.ProcessBatch(ctx)
	if err != nil {
		releaseStale()
		t.Fatalf("check active lease: %v", err)
	}
	if whileLeased.Claimed != 0 {
		releaseStale()
		t.Fatalf("replacement claimed an unexpired lease: %+v", whileLeased)
	}
	time.Sleep(300 * time.Millisecond)

	recovered, err := replacement.ProcessBatch(ctx)
	if err != nil {
		releaseStale()
		t.Fatalf("replacement process expired lease: %v", err)
	}
	if recovered.Published != 1 {
		releaseStale()
		t.Fatalf("replacement result = %+v, want one published item", recovered)
	}
	releaseStale()

	select {
	case stale := <-staleResult:
		if stale.err != nil {
			t.Fatalf("stale worker returned error: %v", stale.err)
		}
		if stale.result.Published != 0 || stale.result.LeaseLost != 1 {
			t.Fatalf("stale worker result = %+v, want its publish completion fenced", stale.result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale worker did not finish after replacement publish")
	}

	first := receiveEvent(t, subscriber)
	second := receiveEvent(t, subscriber)
	if first.Message.MessageID != created.Message.MessageID || second.Message.MessageID != created.Message.MessageID {
		t.Fatalf("replacement/stale notifications = %s, %s; want allowed duplicate for %s", first.Message.MessageID, second.Message.MessageID, created.Message.MessageID)
	}
	if synced := syncMessage(t, env.api.Client(), env.api.URL, created.Message.RoomID, created.Message.Seq); synced.MessageID != created.Message.MessageID {
		t.Fatalf("HTTP sync returned %+v, want the one committed message", synced)
	}
}

func TestRetryAfterLostPublishAcknowledgementCanDuplicateTheEvent(t *testing.T) {
	env := newWorkerTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subscriber := env.subscribe(t, ctx, outbox.EventChannel(string(config.EnvironmentLocal)))
	created := createMessage(t, env.api.Client(), env.api.URL)
	var captured bytes.Buffer

	worker := outbox.NewWorkerWithOptions(env.pool, &dropFirstPublishAck{UniversalClient: env.valkey}, config.EnvironmentLocal, outbox.WorkerOptions{
		RetryBaseDelay: 20 * time.Millisecond,
		RetryMaxDelay:  100 * time.Millisecond,
		Logger:         slog.New(slog.NewJSONHandler(&captured, nil)),
	})
	first, err := worker.ProcessBatch(ctx)
	if err != nil {
		t.Fatalf("process ambiguous publish: %v", err)
	}
	if first.RetryScheduled != 1 || first.Published != 0 {
		t.Fatalf("ambiguous publish result = %+v, want retry scheduled", first)
	}
	var brokerFailureLogged bool
	for _, line := range strings.Split(strings.TrimSpace(captured.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode outbox retry log %q: %v", line, err)
		}
		if event["event"] == "outbox.retry" && event["error_code"] == "VALKEY_PUBLISH_FAILED" {
			brokerFailureLogged = true
			break
		}
	}
	if !brokerFailureLogged {
		t.Fatalf("Valkey publish failure was not observable for broker telemetry: %s", captured.String())
	}
	assertEventMatchesMessage(t, receiveEvent(t, subscriber), created.Message)

	deadline := time.Now().Add(3 * time.Second)
	var retried outbox.BatchResult
	for time.Now().Before(deadline) {
		retried, err = worker.ProcessBatch(ctx)
		if err != nil {
			t.Fatalf("retry outbox publish: %v", err)
		}
		if retried.Published == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if retried.Published != 1 {
		t.Fatalf("retry result = %+v, want a published retry", retried)
	}
	assertEventMatchesMessage(t, receiveEvent(t, subscriber), created.Message)
	if synced := syncMessage(t, env.api.Client(), env.api.URL, created.Message.RoomID, created.Message.Seq); synced.MessageID != created.Message.MessageID {
		t.Fatalf("HTTP sync returned %+v, want one durable message after duplicate publish", synced)
	}
}

func newWorkerTestEnv(t *testing.T) *workerTestEnv {
	t.Helper()
	databaseURL := testingEnv(t, "TEST_DATABASE_URL", "")
	valkeyURL := testingEnv(t, "TEST_VALKEY_URL", "")
	pool := isolatedDatabase(t, databaseURL)
	options, err := redis.ParseURL(valkeyURL)
	if err != nil {
		t.Fatalf("parse test Valkey URL: %v", err)
	}
	valkey := redis.NewClient(options)
	t.Cleanup(func() { _ = valkey.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := valkey.Ping(ctx).Err(); err != nil {
		t.Fatalf("connect to test Valkey: %v", err)
	}
	api := httptest.NewServer(httpapi.NewHandler(config.Config{
		Environment:             config.EnvironmentLocal,
		DatabaseURL:             databaseURL,
		ValkeyURL:               valkeyURL,
		EnableLocalTestIdentity: true,
		CursorHMACKey:           "test-only-room-cursor-signing-key-0123456789",
	}, pool, nil))
	t.Cleanup(api.Close)
	return &workerTestEnv{pool: pool, valkey: valkey, api: api}
}

type workerTestEnv struct {
	pool   *pgxpool.Pool
	valkey *redis.Client
	api    *httptest.Server
}

type testEvent struct {
	Type    string          `json:"type"`
	Message messageResponse `json:"message"`
}

func receiveEvent(t *testing.T, subscriber *redis.PubSub) testEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	message, err := subscriber.ReceiveMessage(ctx)
	if err != nil {
		t.Fatalf("receive Valkey event: %v", err)
	}
	var event testEvent
	if err := json.Unmarshal([]byte(message.Payload), &event); err != nil {
		t.Fatalf("decode Valkey event %q: %v", message.Payload, err)
	}
	if event.Type != "message.created" {
		t.Fatalf("event type = %q, want message.created", event.Type)
	}
	return event
}

func assertEventMatchesMessage(t *testing.T, event testEvent, want messageResponse) {
	t.Helper()
	if event.Type != "message.created" || event.Message != want {
		t.Fatalf("event = %+v, want message.created for %+v", event, want)
	}
}

type gatedPublisher struct {
	redis.UniversalClient
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (publisher *gatedPublisher) Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd {
	publisher.once.Do(func() {
		close(publisher.entered)
		select {
		case <-publisher.release:
		case <-ctx.Done():
		}
	})
	return publisher.UniversalClient.Publish(ctx, channel, message)
}

type dropFirstPublishAck struct {
	redis.UniversalClient
	once sync.Once
}

func (publisher *dropFirstPublishAck) Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd {
	command := publisher.UniversalClient.Publish(ctx, channel, message)
	dropAcknowledgement := false
	publisher.once.Do(func() { dropAcknowledgement = true })
	if dropAcknowledgement && command.Err() == nil {
		command.SetErr(errors.New("simulated lost publish acknowledgement"))
	}
	return command
}

func (env *workerTestEnv) subscribe(t *testing.T, ctx context.Context, channel string) *redis.PubSub {
	t.Helper()
	testSubscriber := env.valkey.Subscribe(ctx, channel)
	testContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := testSubscriber.Receive(testContext); err != nil {
		_ = testSubscriber.Close()
		t.Fatalf("subscribe to %s: %v", channel, err)
	}
	t.Cleanup(func() { _ = testSubscriber.Close() })
	return testSubscriber
}

type messageResponse struct {
	MessageID string `json:"message_id"`
	RoomID    string `json:"room_id"`
	Seq       string `json:"seq"`
	Sender    struct {
		UserID      string `json:"user_id"`
		DisplayName string `json:"display_name"`
	} `json:"sender"`
	ClientMessageID string `json:"client_message_id"`
	Text            string `json:"text"`
	CreatedAt       string `json:"created_at"`
}

func createMessage(t *testing.T, client *http.Client, baseURL string) struct {
	Message  messageResponse `json:"message"`
	Replayed bool            `json:"replayed"`
} {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost,
		baseURL+"/api/v1/rooms/"+demo.GroupRoomID+"/messages",
		strings.NewReader(`{"client_message_id":"`+testClientMessageID+`","text":"worker event"}`))
	if err != nil {
		t.Fatalf("create HTTP request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Local-Test-User-ID", demo.UserAID)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send HTTP request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create message status = %d, body = %s", response.StatusCode, body)
	}
	var result struct {
		Message  messageResponse `json:"message"`
		Replayed bool            `json:"replayed"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode HTTP response %s: %v", body, err)
	}
	if result.Replayed {
		t.Fatal("new message was unexpectedly replayed")
	}
	return result
}

func syncMessage(t *testing.T, client *http.Client, baseURL, roomID, sequence string) messageResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet,
		baseURL+"/api/v1/rooms/"+roomID+"/sync?after_seq=0&through_seq="+sequence,
		nil)
	if err != nil {
		t.Fatalf("create sync request: %v", err)
	}
	request.Header.Set("X-Local-Test-User-ID", demo.UserAID)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send sync request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read sync response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", response.StatusCode, body)
	}
	var result struct {
		Items []messageResponse `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode sync response %s: %v", body, err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("sync items = %d, want 1: %s", len(result.Items), body)
	}
	return result.Items[0]
}

func isolatedDatabase(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	var schemaBytes [8]byte
	if _, err := rand.Read(schemaBytes[:]); err != nil {
		admin.Close()
		t.Fatalf("create isolated schema name: %v", err)
	}
	schema := "outbox_test_" + hex.EncodeToString(schemaBytes[:])
	schemaSQL := (pgx.Identifier{schema}).Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaSQL); err != nil {
		admin.Close()
		t.Fatalf("create isolated schema: %v", err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
		t.Fatalf("parse test PostgreSQL URL: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
		t.Fatalf("connect to isolated schema: %v", err)
	}
	if err := migrations.Up(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
		t.Fatalf("apply migrations: %v", err)
	}
	if err := seed.Apply(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
		t.Fatalf("seed test fixtures: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
	})
	return pool
}

func testingEnv(t *testing.T, name, defaultValue string) string {
	t.Helper()
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	if defaultValue == "" {
		t.Skip("set " + name + " to run outbox integration tests")
	}
	return defaultValue
}
