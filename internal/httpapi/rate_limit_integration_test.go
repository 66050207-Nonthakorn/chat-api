package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/demo"
	"cloudchat/internal/realtime"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestMessageSendRateLimitIsSharedAcrossReplicasAndChargesReplay(t *testing.T) {
	env := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	body := `{"client_message_id":"55555555-5555-4555-8555-555555555555","text":"replay counts as a send"}`
	created, createdBody := env.request(t, env.primary, http.MethodPost, path, demo.UserAID, body)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("initial send status=%d body=%s, want 201", created.StatusCode, createdBody)
	}

	results := runMessageRequests(env, 20, demo.UserAID, body, false)
	limited := 0
	retryAfter := ""
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("replay request failed: %v", result.err)
		}
		if result.status == http.StatusTooManyRequests {
			limited++
			if retryAfter == "" {
				retryAfter = result.retryAfter
			}
		} else if result.status != http.StatusOK {
			t.Fatalf("concurrent replay status=%d, want 200 or 429", result.status)
		}
	}
	if limited == 0 {
		t.Fatal("idempotent replay requests did not consume the shared send bucket")
	}
	assertRetryAfter(t, http.Header{"Retry-After": []string{retryAfter}}, 1)

	waitForRetryAfter(t, retryAfter)
	retried, retryBody := env.request(t, env.primary, http.MethodPost, path, demo.UserAID, body)
	if retried.StatusCode != http.StatusOK {
		t.Fatalf("same-message replay after Retry-After status = %d body=%s, want 200", retried.StatusCode, retryBody)
	}
}

func TestRateLimitUsesAuthenticatedPrincipalNotAnArbitraryUserHeader(t *testing.T) {
	env := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	userABody := `{"client_message_id":"77777777-7777-4777-8777-777777777777","text":"user A"}`
	results := runMessageRequests(env, 20, demo.UserAID, userABody, true)
	limited := 0
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("user A request with arbitrary user header failed: %v", result.err)
		}
		if result.status == http.StatusTooManyRequests {
			limited++
		} else if result.status != http.StatusCreated && result.status != http.StatusOK {
			t.Fatalf("user A request status=%d, want 201, 200, or 429", result.status)
		}
	}
	if limited == 0 {
		t.Fatal("unique caller-supplied user headers bypassed the authenticated user's shared bucket")
	}

	userBBody := `{"client_message_id":"88888888-8888-4888-8888-888888888888","text":"user B"}`
	userB, userBBodyBytes := env.request(t, env.replica, http.MethodPost, path, demo.UserBID, userBBody)
	if userB.StatusCode != http.StatusCreated {
		t.Fatalf("user B shared user A's send bucket: status=%d body=%s, want 201", userB.StatusCode, userBBodyBytes)
	}
}

func TestRateLimitBucketsAreIsolatedByOperationAndEnvironment(t *testing.T) {
	firstEnvironment := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	secondEnvironment := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	body := `{"client_message_id":"66666666-6666-4666-8666-666666666666","text":"isolated buckets"}`
	for index := 0; index < 10; index++ {
		server := firstEnvironment.primary
		if index%2 == 1 {
			server = firstEnvironment.replica
		}
		response, data := firstEnvironment.request(t, server, http.MethodPost, path, demo.UserAID, body)
		want := http.StatusOK
		if index == 0 {
			want = http.StatusCreated
		}
		if response.StatusCode != want {
			t.Fatalf("send %d in first environment status=%d body=%s, want %d", index+1, response.StatusCode, data, want)
		}
	}

	read, readBody := firstEnvironment.request(t, firstEnvironment.primary, http.MethodGet, "/api/v1/rooms", demo.UserAID, "")
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read bucket was consumed by sends: status=%d body=%s", read.StatusCode, readBody)
	}

	send, sendBody := secondEnvironment.request(t, secondEnvironment.replica, http.MethodPost, path, demo.UserAID, body)
	if send.StatusCode != http.StatusCreated {
		t.Fatalf("send bucket was shared across environments: status=%d body=%s", send.StatusCode, sendBody)
	}
}

func TestReadRateLimitSharesBurstAcrossReplicasAndRefills(t *testing.T) {
	env := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	const initialBurst = 40
	results := runReadRequests(env, initialBurst)
	for index, result := range results {
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("initial read %d result=%+v, want HTTP 200 within shared burst", index+1, result)
		}
	}

	results = runReadRequests(env, 100)
	limited := 0
	retryAfter := ""
	for _, result := range results {
		if result.err != nil {
			t.Fatalf("read request failed: %v", result.err)
		}
		if result.status == http.StatusTooManyRequests {
			limited++
			if retryAfter == "" {
				retryAfter = result.retryAfter
			}
		}
	}
	if limited == 0 {
		t.Fatal("additional reads across the two API instances were not rate limited")
	}
	waitForRetryAfter(t, retryAfter)
	response, body := env.request(t, env.primary, http.MethodGet, "/api/v1/rooms", demo.UserAID, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read after Retry-After status=%d body=%s, want 200", response.StatusCode, body)
	}
}

func TestWebSocketHandshakeLimitIsSharedAndRejectedBeforeUpgrade(t *testing.T) {
	env := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connections := make([]*websocket.Conn, 0, 5)
	for index := 0; index < 5; index++ {
		server := env.primary
		if index%2 == 1 {
			server = env.replica
		}
		connections = append(connections, dialChatSocket(t, ctx, server.URL, demo.UserAID))
	}
	t.Cleanup(func() {
		for _, connection := range connections {
			_ = connection.Close(websocket.StatusNormalClosure, "test complete")
		}
	})

	_, response, err := dialWebSocket(ctx, env.replica.URL, demo.UserAID, env.replica.URL, []string{"chat.v1"})
	if err == nil || response == nil || response.StatusCode != http.StatusTooManyRequests {
		if response != nil {
			t.Fatalf("6th handshake response status=%d err=%v, want HTTP 429 before upgrade", response.StatusCode, err)
		}
		t.Fatalf("6th handshake err=%v response=%v, want HTTP 429 before upgrade", err, response)
	}
	assertRetryAfter(t, response.Header, 6)
	_ = response.Body.Close()

	waitForRetryAfter(t, response.Header.Get("Retry-After"))
	connection := dialChatSocket(t, ctx, env.primary.URL, demo.UserAID)
	_ = connection.Close(websocket.StatusNormalClosure, "retry allowed")
}

func TestWebSocketCommandBurstIsPerConnectionAndRepeatedExcessCloses1008(t *testing.T) {
	env := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := dialChatSocket(t, ctx, env.primary.URL, demo.UserAID)
	defer first.Close(websocket.StatusNormalClosure, "test complete")

	for index := 0; index < 10; index++ {
		command := websocketCommand{Type: "unsubscribe", RequestID: fmt.Sprintf("00000000-0000-4000-8000-%012x", index+1), RoomID: demo.GroupRoomID}
		if err := wsjson.Write(ctx, first, command); err != nil {
			t.Fatalf("write command within burst %d: %v", index+1, err)
		}
		frame := readSocketFrame(ctx, first)
		if frame.err != nil || !strings.Contains(string(frame.payload), `"type":"unsubscribed"`) {
			t.Fatalf("command %d acknowledgement=%s err=%v, want unsubscribed", index+1, frame.payload, frame.err)
		}
	}

	second := dialChatSocket(t, ctx, env.replica.URL, demo.UserAID)
	defer second.Close(websocket.StatusNormalClosure, "test complete")
	if err := wsjson.Write(ctx, second, websocketCommand{Type: "unsubscribe", RequestID: "00000000-0000-4000-8000-000000000099", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("write first command on second connection: %v", err)
	}
	if frame := readSocketFrame(ctx, second); frame.err != nil || !strings.Contains(string(frame.payload), `"type":"unsubscribed"`) {
		t.Fatalf("second connection response=%s err=%v, want independent allowance", frame.payload, frame.err)
	}

	if err := wsjson.Write(ctx, first, websocketCommand{Type: "unsubscribe", RequestID: "00000000-0000-4000-8000-000000000011", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("write first excess command: %v", err)
	}
	limited := readSocketFrame(ctx, first)
	var errorFrame struct {
		Type  string `json:"type"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if limited.err != nil || json.Unmarshal(limited.payload, &errorFrame) != nil || errorFrame.Type != "error" || errorFrame.Error.Code != "RATE_LIMITED" {
		t.Fatalf("first excess response=%s err=%v, want RATE_LIMITED", limited.payload, limited.err)
	}
	if err := wsjson.Write(ctx, first, websocketCommand{Type: "unsubscribe", RequestID: "00000000-0000-4000-8000-000000000012", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("write repeated excess command: %v", err)
	}
	_, _, err := first.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Fatalf("close status after repeated excess=%d err=%v, want 1008", got, err)
	}
}

func TestRateLimiterFailureFailsClosedForHTTPHandshakeAndUpgradedSocket(t *testing.T) {
	env := newRateLimitedWebSocketTestEnv(t, realtime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	active := dialChatSocket(t, ctx, env.replica.URL, demo.UserAID)
	if err := wsjson.Write(ctx, active, websocketCommand{Type: "unsubscribe", RequestID: "00000000-0000-4000-8000-000000000001", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("send command before limiter outage: %v", err)
	}
	if frame := readSocketFrame(ctx, active); frame.err != nil || !strings.Contains(string(frame.payload), `"type":"unsubscribed"`) {
		t.Fatalf("command before limiter outage=%s err=%v", frame.payload, frame.err)
	}
	if err := env.replicaRateLimitClient.Close(); err != nil {
		t.Fatalf("make replica limiter dependency unavailable: %v", err)
	}
	if err := wsjson.Write(ctx, active, websocketCommand{Type: "unsubscribe", RequestID: "00000000-0000-4000-8000-000000000002", RoomID: demo.GroupRoomID}); err != nil {
		t.Fatalf("write command during limiter outage: %v", err)
	}
	_, _, err := active.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusTryAgainLater {
		t.Fatalf("close status on upgraded socket after limiter outage=%d err=%v, want 1013", got, err)
	}
	_ = active.Close(websocket.StatusNormalClosure, "test complete")

	if err := env.primaryRateLimitClient.Close(); err != nil {
		t.Fatalf("make primary limiter dependency unavailable: %v", err)
	}
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	response, body := env.request(t, env.primary, http.MethodPost, path, demo.UserAID,
		`{"client_message_id":"55555555-5555-4555-8555-555555555555","text":"fail closed"}`)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("send with unavailable limiter status=%d body=%s, want 503", response.StatusCode, body)
	}
	assertRateLimitResponse(t, response, body, 1)

	response, body = env.request(t, env.primary, http.MethodGet, "/api/v1/rooms", demo.UserAID, "")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("read with unavailable limiter status=%d body=%s, want 503", response.StatusCode, body)
	}
	assertRateLimitResponse(t, response, body, 1)

	_, handshake, err := dialWebSocket(ctx, env.primary.URL, demo.UserAID, env.primary.URL, []string{"chat.v1"})
	if err == nil || handshake == nil || handshake.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("handshake with unavailable limiter response=%v err=%v, want HTTP 503 before upgrade", handshake, err)
	}
	assertRetryAfter(t, handshake.Header, 1)
	_ = handshake.Body.Close()
}

type rateRequestResult struct {
	status     int
	retryAfter string
	err        error
}

func rateLimitedRequest(server *httptest.Server, method, url, authenticatedUser, claimedUser, body string) (rateRequestResult, []byte) {
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return rateRequestResult{err: err}, nil
	}
	request.Header.Set(auth.LocalTestUserHeader, authenticatedUser)
	if claimedUser != "" {
		request.Header.Set("X-User-ID", claimedUser)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		return rateRequestResult{err: err}, nil
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	return rateRequestResult{status: response.StatusCode, retryAfter: response.Header.Get("Retry-After"), err: err}, data
}

func runConcurrentRateRequests(env *webSocketTestEnv, count int, send func(*httptest.Server, int) rateRequestResult) []rateRequestResult {
	results := make([]rateRequestResult, count)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			server := env.primary
			if index%2 == 1 {
				server = env.replica
			}
			results[index] = send(server, index)
		}(index)
	}
	close(start)
	wait.Wait()
	return results
}

func runMessageRequests(env *webSocketTestEnv, count int, userID, body string, forgeUserHeader bool) []rateRequestResult {
	path := "/api/v1/rooms/" + demo.GroupRoomID + "/messages"
	return runConcurrentRateRequests(env, count, func(server *httptest.Server, index int) rateRequestResult {
		claimedUser := ""
		if forgeUserHeader {
			claimedUser = fmt.Sprintf("attacker-selected-%d", index)
		}
		result, _ := rateLimitedRequest(server, http.MethodPost, server.URL+path, userID, claimedUser, body)
		return result
	})
}

func runReadRequests(env *webSocketTestEnv, count int) []rateRequestResult {
	return runConcurrentRateRequests(env, count, func(server *httptest.Server, _ int) rateRequestResult {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/rooms", nil)
		if err != nil {
			return rateRequestResult{err: err}
		}
		request.Header.Set(auth.LocalTestUserHeader, demo.UserAID)
		response, err := server.Client().Do(request)
		if err != nil {
			return rateRequestResult{err: err}
		}
		defer response.Body.Close()
		return rateRequestResult{status: response.StatusCode, retryAfter: response.Header.Get("Retry-After")}
	})
}

func assertRateLimitResponse(t *testing.T, response *http.Response, body []byte, minimumRetryAfter int) {
	t.Helper()
	wantCode := ""
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		wantCode = "RATE_LIMITED"
	case http.StatusServiceUnavailable:
		wantCode = "DEPENDENCY_UNAVAILABLE"
	default:
		t.Fatalf("rate-limit response status=%d, want 429 or 503", response.StatusCode)
	}
	var decoded struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Error.Code != wantCode {
		t.Fatalf("rate-limit response status=%d body=%s, want %s", response.StatusCode, body, wantCode)
	}
	assertRetryAfter(t, response.Header, minimumRetryAfter)
}

func assertRetryAfter(t *testing.T, headers http.Header, minimum int) {
	t.Helper()
	value, err := strconv.Atoi(headers.Get("Retry-After"))
	if err != nil || value < minimum {
		t.Fatalf("Retry-After=%q, want integer >= %d seconds", headers.Get("Retry-After"), minimum)
	}
}

func waitForRetryAfter(t *testing.T, header string) {
	t.Helper()
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds < 1 {
		t.Fatalf("Retry-After=%q, want positive integer seconds", header)
	}
	timer := time.NewTimer(time.Duration(seconds)*time.Second + 100*time.Millisecond)
	defer timer.Stop()
	<-timer.C
}
