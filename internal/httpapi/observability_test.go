package httpapi_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloudchat/internal/config"
	"cloudchat/internal/demo"
	"cloudchat/internal/httpapi"
)

func TestPublicHTTPLogsCorrelateRequestWithoutSensitiveRequestData(t *testing.T) {
	var captured bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&captured, nil))
	handler := httpapi.NewHandler(config.Config{Environment: config.EnvironmentLocal}, nil, httpapi.Options{Logger: logger})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/unknown?code=query-secret", strings.NewReader("private message body"))
	request.Header.Set("Cookie", "session=session-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(captured.Bytes()), &event); err != nil {
		t.Fatalf("decode structured log %q: %v", captured.String(), err)
	}
	if event["event"] != "http.request" {
		t.Fatalf("event = %v, want http.request", event["event"])
	}
	if event["request_id"] != response.Header().Get("X-Request-ID") {
		t.Fatalf("logged request ID = %v, response request ID = %q", event["request_id"], response.Header().Get("X-Request-ID"))
	}
	if event["status"] != float64(http.StatusNotFound) || event["route"] != "not_found" {
		t.Fatalf("request fields = %+v, want safe route and status", event)
	}
	for _, sensitive := range []string{"query-secret", "session-secret", "private message body"} {
		if strings.Contains(captured.String(), sensitive) {
			t.Errorf("structured log exposed sensitive request data %q: %s", sensitive, captured.String())
		}
	}
}

func TestRoomSyncRequestsLogCorrelatedLatencyWithSafeRouteName(t *testing.T) {
	var captured bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&captured, nil))
	test := newTestServer(t, httpapi.Options{Logger: logger})
	response, _ := test.request(t, http.MethodGet,
		"/api/v1/rooms/"+demo.GroupRoomID+"/sync?after_seq=0&through_seq=0", demo.UserAID, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("sync status = %d, want 200", response.StatusCode)
	}

	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(captured.Bytes()), &event); err != nil {
		t.Fatalf("decode sync request log %q: %v", captured.String(), err)
	}
	if event["event"] != "http.request" || event["route"] != "api.room.sync" || event["status"] != float64(http.StatusOK) {
		t.Fatalf("sync request fields = %+v, want safe route and status", event)
	}
	if event["request_id"] != response.Header.Get("X-Request-ID") {
		t.Fatalf("sync log request ID = %v, response request ID = %q", event["request_id"], response.Header.Get("X-Request-ID"))
	}
	if _, ok := event["duration_ms"].(float64); !ok {
		t.Fatalf("sync log has no numeric duration: %+v", event)
	}
	if strings.Contains(captured.String(), demo.GroupRoomID) || strings.Contains(captured.String(), "after_seq") {
		t.Fatalf("sync request log contains room path or query details: %s", captured.String())
	}
}

func TestHandlersUseIndependentRequestLoggers(t *testing.T) {
	defaultLogger := slog.Default()
	var captured [2]bytes.Buffer
	var handlers [2]http.Handler
	for i := range handlers {
		logger := slog.New(slog.NewJSONHandler(&captured[i], nil)).With("logger_owner", i)
		handlers[i] = httpapi.NewHandler(config.Config{Environment: config.EnvironmentLocal}, nil, httpapi.Options{Logger: logger})
	}
	if slog.Default() != defaultLogger {
		t.Fatal("constructing handlers replaced the process logger")
	}
	for i, handler := range handlers {
		request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var event map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(captured[i].Bytes()), &event); err != nil {
			t.Fatalf("decode handler %d log %q: %v", i, captured[i].String(), err)
		}
		if event["logger_owner"] != float64(i) || event["request_id"] != response.Header().Get("X-Request-ID") {
			t.Fatalf("handler %d log = %+v, want its own logger and request ID", i, event)
		}
	}
}
