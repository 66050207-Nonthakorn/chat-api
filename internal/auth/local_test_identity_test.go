package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/demo"
)

func TestLocalTestIdentityCanSelectOnlySeededDemoUsers(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(principal.UserID))
	})
	handler := auth.LocalTestIdentityMiddleware(config.Config{
		Environment:             config.EnvironmentLocal,
		EnableLocalTestIdentity: true,
	}, next)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	request.Header.Set(auth.LocalTestUserHeader, demo.UserAID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); got != demo.UserAID {
		t.Fatalf("identity = %q, want seeded user %q", got, demo.UserAID)
	}
}

func TestLocalTestIdentityDoesNotRunOutsideLocalMode(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.PrincipalFromContext(r.Context()); ok {
			http.Error(w, "identity unexpectedly accepted", http.StatusInternalServerError)
			return
		}
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
	})
	handler := auth.LocalTestIdentityMiddleware(config.Config{
		Environment:             config.EnvironmentProduction,
		EnableLocalTestIdentity: true,
	}, next)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	request.Header.Set(auth.LocalTestUserHeader, demo.UserAID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestLocalTestIdentityRejectsUnknownUsers(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.PrincipalFromContext(r.Context()); ok {
			http.Error(w, "unknown identity accepted", http.StatusInternalServerError)
			return
		}
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
	})
	handler := auth.LocalTestIdentityMiddleware(config.Config{
		Environment:             config.EnvironmentLocal,
		EnableLocalTestIdentity: true,
	}, next)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/session", nil)
	request.Header.Set(auth.LocalTestUserHeader, "99999999-9999-4999-8999-999999999999")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}
