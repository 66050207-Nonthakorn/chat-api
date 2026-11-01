package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloudchat/internal/health"
)

func TestLiveHealthDoesNotDependOnExternalServices(t *testing.T) {
	readinessCalled := false
	handler := health.NewHandler(func(context.Context) error {
		readinessCalled = true
		return errors.New("database password=private")
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	request.Header.Set("X-Request-ID", "client-supplied")
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if readinessCalled {
		t.Fatal("liveness check called dependency readiness")
	}
	if got := response.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q, want only the healthy status", got)
	}
	assertNoStore(t, response)
	assertRequestID(t, response)
	if got := response.Header().Get("X-Request-ID"); got == "client-supplied" {
		t.Fatal("server reused a client-supplied request ID")
	}
}

func TestReadyHealthReturnsUnavailableWithoutLeakingDependencyError(t *testing.T) {
	handler := health.NewHandler(func(context.Context) error {
		return errors.New("database password=private")
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if got := response.Body.String(); got != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("body = %q, want only the unavailable status", got)
	}
	if strings.Contains(response.Body.String(), "private") {
		t.Fatal("health response exposed an internal dependency error")
	}
	assertNoStore(t, response)
	assertRequestID(t, response)
}

func TestReadyHealthReturnsOKWhenDependenciesAreReady(t *testing.T) {
	handler := health.NewHandler(func(context.Context) error { return nil })

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q, want healthy status", got)
	}
	assertNoStore(t, response)
	assertRequestID(t, response)
}

func assertNoStore(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func assertRequestID(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("X-Request-ID"); got == "" {
		t.Error("X-Request-ID is missing")
	}
}
