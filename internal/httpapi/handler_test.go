package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"cloudchat/internal/config"
	"cloudchat/internal/demo"
	"cloudchat/internal/httpapi"
	"cloudchat/internal/migrations"
	"cloudchat/internal/seed"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testServer struct {
	server *httptest.Server
	pool   *pgxpool.Pool
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL-backed HTTP tests")
	}

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
	schema := "http_test_" + hex.EncodeToString(schemaBytes[:])
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
		t.Fatalf("connect to isolated test schema: %v", err)
	}

	if err := migrations.Up(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
		t.Fatalf("apply migrations in isolated test schema: %v", err)
	}
	if err := seed.Apply(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
		t.Fatalf("seed isolated test schema: %v", err)
	}

	cfg := config.Config{
		Environment:             config.EnvironmentLocal,
		DatabaseURL:             databaseURL,
		ValkeyURL:               "redis://localhost:6379/0",
		HTTPAddr:                "127.0.0.1:0",
		EnableLocalTestIdentity: true,
		CursorHMACKey:           "test-only-room-cursor-signing-key-0123456789",
	}
	server := httptest.NewServer(httpapi.NewHandler(cfg, pool, nil))
	test := &testServer{server: server, pool: pool}
	t.Cleanup(func() {
		server.Close()
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schemaSQL+" CASCADE")
		admin.Close()
	})
	return test
}

func TestRoomsArePaginatedByKeysetAndCursorIsBoundToUser(t *testing.T) {
	test := newTestServer(t)

	firstResponse, firstBody := test.request(t, http.MethodGet, "/api/v1/rooms?limit=1", demo.UserAID, "")
	if firstResponse.StatusCode != http.StatusOK {
		t.Fatalf("first page status = %d, body = %s", firstResponse.StatusCode, firstBody)
	}
	assertResponseMetadata(t, firstResponse)

	var first struct {
		Items      []roomResponse `json:"items"`
		NextCursor *string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstBody, &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].RoomID != demo.GroupRoomID {
		t.Fatalf("first items = %+v, want the first room by ID", first.Items)
	}
	if first.Items[0].MembershipRole != "owner" || first.Items[0].LastSeq != "0" {
		t.Fatalf("first room = %+v, want owner role and zero last sequence", first.Items[0])
	}
	if first.NextCursor == nil || *first.NextCursor == "" {
		t.Fatal("first page cursor is empty")
	}

	secondPath := "/api/v1/rooms?limit=1&cursor=" + url.QueryEscape(*first.NextCursor)
	secondResponse, secondBody := test.request(t, http.MethodGet, secondPath, demo.UserAID, "")
	if secondResponse.StatusCode != http.StatusOK {
		t.Fatalf("second page status = %d, body = %s", secondResponse.StatusCode, secondBody)
	}
	var second struct {
		Items      []roomResponse `json:"items"`
		NextCursor *string        `json:"next_cursor"`
	}
	if err := json.Unmarshal(secondBody, &second); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].RoomID != demo.DirectRoomID {
		t.Fatalf("second items = %+v, want the next room by ID", second.Items)
	}
	if second.Items[0].MembershipRole != "member" || second.NextCursor != nil {
		t.Fatalf("second page = %+v, want member role and no further cursor", second)
	}

	otherUserResponse, otherUserBody := test.request(t, http.MethodGet, secondPath, demo.UserBID, "")
	if otherUserResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("other user's cursor status = %d, body = %s; want 400", otherUserResponse.StatusCode, otherUserBody)
	}
	assertErrorCode(t, otherUserBody, "INVALID_CURSOR")
	malformedResponse, malformedBody := test.request(t, http.MethodGet, "/api/v1/rooms?cursor=%ZZ", demo.UserAID, "")
	if malformedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed query status = %d, body = %s", malformedResponse.StatusCode, malformedBody)
	}
	assertErrorCode(t, malformedBody, "INVALID_REQUEST")
}

func TestSendReplayHistoryAndSyncUsePersistedHTTPState(t *testing.T) {
	test := newTestServer(t)
	const clientMessageID = "55555555-5555-4555-8555-555555555555"
	body := `{"client_message_id":"` + clientMessageID + `","text":"hello"}`
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"

	createdResponse, createdBody := test.request(t, http.MethodPost, path, demo.UserAID, body)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdResponse.StatusCode, createdBody)
	}
	assertResponseMetadata(t, createdResponse)
	var created sendMessageResponse
	if err := json.Unmarshal(createdBody, &created); err != nil {
		t.Fatalf("decode created message: %v", err)
	}
	if created.Replayed || created.Message.Seq != "1" || created.Message.Text != "hello" || created.Message.Sender.UserID != demo.UserAID || created.Message.Sender.DisplayName != "Demo A" {
		t.Fatalf("created response = %+v", created)
	}

	replayResponse, replayBody := test.request(t, http.MethodPost, path, demo.UserAID, body)
	if replayResponse.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, body = %s", replayResponse.StatusCode, replayBody)
	}
	var replay sendMessageResponse
	if err := json.Unmarshal(replayBody, &replay); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if !replay.Replayed || replay.Message.MessageID != created.Message.MessageID || replay.Message.Seq != created.Message.Seq {
		t.Fatalf("replay = %+v; want original message %+v", replay, created.Message)
	}

	conflictBody := `{"client_message_id":"` + clientMessageID + `","text":"different"}`
	conflictResponse, conflictResponseBody := test.request(t, http.MethodPost, path, demo.UserAID, conflictBody)
	if conflictResponse.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", conflictResponse.StatusCode, conflictResponseBody)
	}
	assertErrorCode(t, conflictResponseBody, "IDEMPOTENCY_CONFLICT")

	historyResponse, historyBody := test.request(t, http.MethodGet, path, demo.UserAID, "")
	if historyResponse.StatusCode != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", historyResponse.StatusCode, historyBody)
	}
	var history pageResponse
	if err := json.Unmarshal(historyBody, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Items) != 1 || history.Items[0].MessageID != created.Message.MessageID || history.HasMore || history.NextBeforeSeq != nil {
		t.Fatalf("history = %+v", history)
	}

	syncResponse, syncBody := test.request(t, http.MethodGet, "/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=0", demo.UserAID, "")
	if syncResponse.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", syncResponse.StatusCode, syncBody)
	}
	var syncPage syncResponseBody
	if err := json.Unmarshal(syncBody, &syncPage); err != nil {
		t.Fatalf("decode sync: %v", err)
	}
	if len(syncPage.Items) != 1 || syncPage.Items[0].MessageID != created.Message.MessageID || syncPage.ThroughSeq != "1" || syncPage.NextAfterSeq != "1" || syncPage.HasMore {
		t.Fatalf("sync = %+v", syncPage)
	}
}

func TestConcurrentRetriesReturnOneLogicalMessage(t *testing.T) {
	test := newTestServer(t)
	const clientMessageID = "66666666-6666-4666-8666-666666666666"
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	body := `{"client_message_id":"` + clientMessageID + `","text":"concurrent"}`

	type result struct {
		status int
		body   []byte
		err    error
	}
	results := make(chan result, 100)
	var start sync.WaitGroup
	start.Add(1)
	for index := 0; index < 100; index++ {
		go func() {
			start.Wait()
			request, err := http.NewRequest(http.MethodPost, test.server.URL+path, strings.NewReader(body))
			if err != nil {
				results <- result{err: err}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Local-Test-User-ID", demo.UserAID)
			response, err := test.server.Client().Do(request)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer response.Body.Close()
			responseBody, err := io.ReadAll(response.Body)
			results <- result{status: response.StatusCode, body: responseBody, err: err}
		}()
	}
	start.Done()

	created := 0
	replayed := 0
	messageIDs := map[string]bool{}
	for index := 0; index < 100; index++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent request error: %v", result.err)
		}
		var response sendMessageResponse
		if err := json.Unmarshal(result.body, &response); err != nil {
			t.Fatalf("decode concurrent response: %v; body=%s", err, result.body)
		}
		messageIDs[response.Message.MessageID] = true
		switch result.status {
		case http.StatusCreated:
			created++
			if response.Replayed {
				t.Error("created response marked as replay")
			}
		case http.StatusOK:
			replayed++
			if !response.Replayed {
				t.Error("replay response did not mark replayed")
			}
		default:
			t.Fatalf("concurrent status = %d, body=%s", result.status, result.body)
		}
		if response.Message.Seq != "1" {
			t.Errorf("concurrent message seq = %q, want 1", response.Message.Seq)
		}
	}
	if created != 1 || replayed != 99 || len(messageIDs) != 1 {
		t.Fatalf("created=%d replayed=%d unique message IDs=%d", created, replayed, len(messageIDs))
	}

	historyResponse, historyBody := test.request(t, http.MethodGet, path, demo.UserAID, "")
	if historyResponse.StatusCode != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", historyResponse.StatusCode, historyBody)
	}
	var history pageResponse
	if err := json.Unmarshal(historyBody, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Items) != 1 || history.Items[0].Seq != "1" {
		t.Fatalf("history after concurrent retries = %+v", history)
	}
}

func TestRetryAfterUnknownResponseOutcomeReturnsStoredMessage(t *testing.T) {
	test := newTestServer(t)
	const clientMessageID = "77777777-7777-4777-8777-777777777777"
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	body := `{"client_message_id":"` + clientMessageID + `","text":"response lost"}`
	request, err := http.NewRequest(http.MethodPost, test.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Local-Test-User-ID", demo.UserAID)
	client := &http.Client{Transport: discardResponseTransport{base: http.DefaultTransport}}
	if _, err := client.Do(request); err == nil {
		t.Fatal("simulated response loss returned no client error")
	}

	retryResponse, retryBody := test.request(t, http.MethodPost, path, demo.UserAID, body)
	if retryResponse.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, body = %s", retryResponse.StatusCode, retryBody)
	}
	var retry sendMessageResponse
	if err := json.Unmarshal(retryBody, &retry); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if !retry.Replayed || retry.Message.Seq != "1" {
		t.Fatalf("retry response = %+v", retry)
	}
}

func TestMembershipHidesRoomAndBlocksReplayAndReads(t *testing.T) {
	test := newTestServer(t)
	const clientMessageID = "88888888-8888-4888-8888-888888888888"
	messagePath := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	body := `{"client_message_id":"` + clientMessageID + `","text":"private"}`
	createdResponse, createdBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, body)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdResponse.StatusCode, createdBody)
	}

	if _, err := test.pool.Exec(context.Background(), `DELETE FROM room_memberships WHERE room_id = $1::uuid AND user_id = $2::uuid`, demo.GroupRoomID, demo.UserBID); err != nil {
		t.Fatalf("remove fixture membership: %v", err)
	}
	for _, testCase := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: messagePath},
		{method: http.MethodGet, path: "/api/v1/rooms/" + demo.GroupRoomID + "/sync?after_seq=0"},
		{method: http.MethodPost, path: messagePath, body: body},
	} {
		response, responseBody := test.request(t, testCase.method, testCase.path, demo.UserBID, testCase.body)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("unauthorized %s %s status = %d, body = %s", testCase.method, testCase.path, response.StatusCode, responseBody)
		}
		assertErrorCode(t, responseBody, "ROOM_NOT_FOUND")
		assertResponseMetadata(t, response)
	}
	missingResponse, missingBody := test.request(t, http.MethodGet, "/api/v1/rooms/99999999-9999-4999-8999-999999999999/messages", demo.UserBID, "")
	if missingResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("missing room status = %d, body = %s", missingResponse.StatusCode, missingBody)
	}
	assertErrorCode(t, missingBody, "ROOM_NOT_FOUND")
}

func TestHistoryAndSyncPaginationRejectInvalidCursorsAndKeepSnapshots(t *testing.T) {
	test := newTestServer(t)
	messagePath := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	for index := 0; index < 3; index++ {
		clientMessageID := fmt.Sprintf("99999999-9999-4999-9999-%012d", index+1)
		body := `{"client_message_id":"` + clientMessageID + `","text":"item"}`
		response, responseBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, body)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("send %d status = %d, body = %s", index+1, response.StatusCode, responseBody)
		}
	}

	historyResponse, historyBody := test.request(t, http.MethodGet, messagePath+"?limit=2", demo.UserAID, "")
	if historyResponse.StatusCode != http.StatusOK {
		t.Fatalf("history status = %d, body = %s", historyResponse.StatusCode, historyBody)
	}
	var history pageResponse
	if err := json.Unmarshal(historyBody, &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history.Items) != 2 || history.Items[0].Seq != "3" || history.Items[1].Seq != "2" || !history.HasMore || history.NextBeforeSeq == nil || *history.NextBeforeSeq != "2" {
		t.Fatalf("first history page = %+v", history)
	}
	olderResponse, olderBody := test.request(t, http.MethodGet, messagePath+"?before_seq=2&limit=2", demo.UserAID, "")
	if olderResponse.StatusCode != http.StatusOK {
		t.Fatalf("older history status = %d, body = %s", olderResponse.StatusCode, olderBody)
	}
	var older pageResponse
	if err := json.Unmarshal(olderBody, &older); err != nil {
		t.Fatalf("decode older history: %v", err)
	}
	if len(older.Items) != 1 || older.Items[0].Seq != "1" || older.HasMore || older.NextBeforeSeq != nil {
		t.Fatalf("older history page = %+v", older)
	}

	firstSyncResponse, firstSyncBody := test.request(t, http.MethodGet, "/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=0&limit=2", demo.UserAID, "")
	if firstSyncResponse.StatusCode != http.StatusOK {
		t.Fatalf("first sync status = %d, body = %s", firstSyncResponse.StatusCode, firstSyncBody)
	}
	var firstSync syncResponseBody
	if err := json.Unmarshal(firstSyncBody, &firstSync); err != nil {
		t.Fatalf("decode first sync: %v", err)
	}
	if len(firstSync.Items) != 2 || firstSync.Items[0].Seq != "1" || firstSync.Items[1].Seq != "2" || firstSync.ThroughSeq != "3" || firstSync.NextAfterSeq != "2" || !firstSync.HasMore {
		t.Fatalf("first sync page = %+v", firstSync)
	}

	newBody := `{"client_message_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","text":"after snapshot"}`
	newResponse, newResponseBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, newBody)
	if newResponse.StatusCode != http.StatusCreated {
		t.Fatalf("send after snapshot status = %d, body = %s", newResponse.StatusCode, newResponseBody)
	}
	secondSyncPath := "/api/v1/rooms/" + demo.GroupRoomID + "/sync?after_seq=2&limit=2&through_seq=3"
	secondSyncResponse, secondSyncBody := test.request(t, http.MethodGet, secondSyncPath, demo.UserAID, "")
	if secondSyncResponse.StatusCode != http.StatusOK {
		t.Fatalf("second sync status = %d, body = %s", secondSyncResponse.StatusCode, secondSyncBody)
	}
	var secondSync syncResponseBody
	if err := json.Unmarshal(secondSyncBody, &secondSync); err != nil {
		t.Fatalf("decode second sync: %v", err)
	}
	if len(secondSync.Items) != 1 || secondSync.Items[0].Seq != "3" || secondSync.ThroughSeq != "3" || secondSync.NextAfterSeq != "3" || secondSync.HasMore {
		t.Fatalf("second sync page = %+v", secondSync)
	}
	latestSyncResponse, latestSyncBody := test.request(t, http.MethodGet, "/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=3", demo.UserAID, "")
	if latestSyncResponse.StatusCode != http.StatusOK {
		t.Fatalf("latest sync status = %d, body = %s", latestSyncResponse.StatusCode, latestSyncBody)
	}
	var latestSync syncResponseBody
	if err := json.Unmarshal(latestSyncBody, &latestSync); err != nil {
		t.Fatalf("decode latest sync: %v", err)
	}
	if len(latestSync.Items) != 1 || latestSync.Items[0].Seq != "4" || latestSync.ThroughSeq != "4" {
		t.Fatalf("latest sync = %+v", latestSync)
	}

	for _, path := range []string{
		"/api/v1/rooms/" + demo.GroupRoomID + "/sync",
		"/api/v1/rooms/" + demo.GroupRoomID + "/sync?after_seq=01",
		"/api/v1/rooms/" + demo.GroupRoomID + "/sync?after_seq=5",
		"/api/v1/rooms/" + demo.GroupRoomID + "/sync?after_seq=0&through_seq=5",
		"/api/v1/rooms/" + demo.GroupRoomID + "/sync?after_seq=2&through_seq=1",
	} {
		response, responseBody := test.request(t, http.MethodGet, path, demo.UserAID, "")
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid cursor path %s status = %d, body = %s", path, response.StatusCode, responseBody)
		}
		assertErrorCode(t, responseBody, "INVALID_CURSOR")
	}
}

func TestSyncReportsASequenceGap(t *testing.T) {
	test := newTestServer(t)
	messagePath := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	for index := 1; index <= 3; index++ {
		clientMessageID := fmt.Sprintf("bbbbbbbb-bbbb-4bbb-8bbb-%012d", index)
		body := `{"client_message_id":"` + clientMessageID + `","text":"item"}`
		response, responseBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, body)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("send %d status = %d, body = %s", index, response.StatusCode, responseBody)
		}
	}
	if _, err := test.pool.Exec(context.Background(), `DELETE FROM message_outbox WHERE message_id = (SELECT message_id FROM messages WHERE room_id = $1::uuid AND seq = 2)`, demo.GroupRoomID); err != nil {
		t.Fatalf("delete fixture outbox event: %v", err)
	}
	if _, err := test.pool.Exec(context.Background(), `DELETE FROM messages WHERE room_id = $1::uuid AND seq = 2`, demo.GroupRoomID); err != nil {
		t.Fatalf("create fixture sequence gap: %v", err)
	}

	response, responseBody := test.request(t, http.MethodGet, "/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=1&through_seq=3", demo.UserAID, "")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("gap sync status = %d, body = %s", response.StatusCode, responseBody)
	}
	assertErrorCode(t, responseBody, "SYNC_INCONSISTENT")
}

func TestSyncReportsAMissingTailSequence(t *testing.T) {
	test := newTestServer(t)
	messagePath := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	for index := 1; index <= 2; index++ {
		clientMessageID := fmt.Sprintf("eeeeeeee-eeee-4eee-8eee-%012d", index)
		body := `{"client_message_id":"` + clientMessageID + `","text":"item"}`
		response, responseBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, body)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("send %d status = %d, body = %s", index, response.StatusCode, responseBody)
		}
	}
	if _, err := test.pool.Exec(context.Background(), `DELETE FROM message_outbox WHERE message_id = (SELECT message_id FROM messages WHERE room_id = $1::uuid AND seq = 2)`, demo.GroupRoomID); err != nil {
		t.Fatalf("delete fixture outbox event: %v", err)
	}
	if _, err := test.pool.Exec(context.Background(), `DELETE FROM messages WHERE room_id = $1::uuid AND seq = 2`, demo.GroupRoomID); err != nil {
		t.Fatalf("create fixture tail gap: %v", err)
	}

	response, responseBody := test.request(t, http.MethodGet, "/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=0&through_seq=2", demo.UserAID, "")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("tail-gap sync status = %d, body = %s", response.StatusCode, responseBody)
	}
	assertErrorCode(t, responseBody, "SYNC_INCONSISTENT")
}

func TestOutboxFailureRollsBackMessageAndSequence(t *testing.T) {
	test := newTestServer(t)
	if _, err := test.pool.Exec(context.Background(), `
		CREATE FUNCTION fail_outbox_for_test() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced outbox failure'; END
		$$;
		CREATE TRIGGER fail_outbox_for_test BEFORE INSERT ON message_outbox
		FOR EACH ROW EXECUTE FUNCTION fail_outbox_for_test()`); err != nil {
		t.Fatalf("install outbox failure fixture: %v", err)
	}
	const clientMessageID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	messagePath := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	body := `{"client_message_id":"` + clientMessageID + `","text":"retry after rollback"}`
	failedResponse, failedBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, body)
	if failedResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failed send status = %d, body = %s", failedResponse.StatusCode, failedBody)
	}
	if _, err := test.pool.Exec(context.Background(), `DROP TRIGGER fail_outbox_for_test ON message_outbox; DROP FUNCTION fail_outbox_for_test()`); err != nil {
		t.Fatalf("remove outbox failure fixture: %v", err)
	}

	retryResponse, retryBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, body)
	if retryResponse.StatusCode != http.StatusCreated {
		t.Fatalf("retry status = %d, body = %s", retryResponse.StatusCode, retryBody)
	}
	var created sendMessageResponse
	if err := json.Unmarshal(retryBody, &created); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if created.Message.Seq != "1" {
		t.Fatalf("retry sequence = %q, want 1", created.Message.Seq)
	}
}

func TestSendValidationAndAuthenticationUseCommonErrorEnvelope(t *testing.T) {
	test := newTestServer(t)
	messagePath := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	unauthenticated, unauthenticatedBody := test.request(t, http.MethodGet, "/api/v1/rooms", "", "")
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, body = %s", unauthenticated.StatusCode, unauthenticatedBody)
	}
	assertErrorCode(t, unauthenticatedBody, "UNAUTHENTICATED")
	assertResponseMetadata(t, unauthenticated)

	request, err := http.NewRequest(http.MethodPost, test.server.URL+messagePath, strings.NewReader(`{"client_message_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Local-Test-User-ID", demo.UserAID)
	request.Header.Set("Content-Type", "text/plain")
	response, err := test.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("unsupported media type status = %d", response.StatusCode)
	}
	assertResponseMetadata(t, response)

	for _, body := range []string{
		`{"client_message_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","text":"   "}`,
		`{"client_message_id":"not-a-uuid","text":"hello"}`,
		`{"client_message_id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd","text":"hello","extra":true}`,
	} {
		result, resultBody := test.request(t, http.MethodPost, messagePath, demo.UserAID, body)
		if result.StatusCode != http.StatusUnprocessableEntity && result.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid request status = %d, body = %s", result.StatusCode, resultBody)
		}
	}
}

type sendMessageResponse struct {
	Message  messageResponse `json:"message"`
	Replayed bool            `json:"replayed"`
}

type messageResponse struct {
	MessageID       string                `json:"message_id"`
	RoomID          string                `json:"room_id"`
	Seq             string                `json:"seq"`
	Sender          messageSenderResponse `json:"sender"`
	ClientMessageID string                `json:"client_message_id"`
	Text            string                `json:"text"`
	CreatedAt       string                `json:"created_at"`
}

type messageSenderResponse struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

type pageResponse struct {
	Items         []messageResponse `json:"items"`
	NextBeforeSeq *string           `json:"next_before_seq"`
	HasMore       bool              `json:"has_more"`
}

type syncResponseBody struct {
	Items        []messageResponse `json:"items"`
	ThroughSeq   string            `json:"through_seq"`
	NextAfterSeq string            `json:"next_after_seq"`
	HasMore      bool              `json:"has_more"`
}

type discardResponseTransport struct {
	base http.RoundTripper
}

func (transport discardResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	return nil, errors.New("simulated response loss")
}

type roomResponse struct {
	RoomID         string `json:"room_id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	MembershipRole string `json:"membership_role"`
	LastSeq        string `json:"last_seq"`
}

func (s *testServer) request(t *testing.T, method, path, userID, body string) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, s.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("create HTTP request: %v", err)
	}
	request.Header.Set("X-Local-Test-User-ID", userID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.server.Client().Do(request)
	if err != nil {
		t.Fatalf("send HTTP request: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP response: %v", err)
	}
	return response, responseBody
}

func assertResponseMetadata(t *testing.T, response *http.Response) {
	t.Helper()
	if requestID := response.Header.Get("X-Request-ID"); requestID == "" {
		t.Error("response has no X-Request-ID")
	}
	if cacheControl := response.Header.Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cacheControl)
	}
}

func assertErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode error response %s: %v", body, err)
	}
	if response.Error.Code != want {
		t.Errorf("error code = %q, want %q", response.Error.Code, want)
	}
}
