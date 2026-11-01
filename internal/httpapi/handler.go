package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"cloudchat/internal/apierror"
	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/health"
	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type handler struct {
	config    config.Config
	pool      *pgxpool.Pool
	websocket http.Handler
}

// NewHandler wires the public HTTP API and health endpoints. Authentication
// is supplied by the configured identity middleware; production session auth
// is added by the session implementation.
func NewHandler(cfg config.Config, pool *pgxpool.Pool, readiness health.ReadinessCheck) http.Handler {
	return NewHandlerWithRealtime(cfg, pool, readiness, nil)
}

// NewHandlerWithRealtime wires the HTTP API and, when supplied, the local
// WebSocket subscriber. The WebSocket service owns its own process lifecycle.
func NewHandlerWithRealtime(cfg config.Config, pool *pgxpool.Pool, readiness health.ReadinessCheck, websocketHandler http.Handler) http.Handler {
	api := &handler{config: cfg, pool: pool, websocket: websocketHandler}
	healthHandler := health.NewHandler(readiness)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			healthHandler.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.serveHTTP(w, r)
			return
		}
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
	})
	return requestMetadata(auth.LocalTestIdentityMiddleware(cfg, root))
}

func (h *handler) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/ws" {
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			return
		}
		if h.websocket == nil {
			dependencyUnavailable(w, r)
			return
		}
		h.websocket.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/api/v1/rooms" {
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			return
		}
		if _, ok := requirePrincipal(w, r); !ok {
			return
		}
		h.listRooms(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/rooms/") {
		if _, ok := requirePrincipal(w, r); !ok {
			return
		}
		h.serveRoom(w, r)
		return
	}
	writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
}

func requirePrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required", false, nil)
		return auth.Principal{}, false
	}
	return principal, true
}

type errorDetail = apierror.Detail

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, details []errorDetail) {
	apierror.Write(w, status, code, message, retryable, details)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func requestMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", requestID())
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

var fallbackRequestSequence atomic.Uint64

func requestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%d-%d", time.Now().UTC().UnixNano(), fallbackRequestSequence.Add(1))
}

func dependencyUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
}

func (h *handler) serveRoom(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/rooms/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !uuid.IsCanonical(parts[0]) {
		writeError(w, r, http.StatusNotFound, "ROOM_NOT_FOUND", "Room not found", false, nil)
		return
	}
	switch parts[1] {
	case "messages":
		switch r.Method {
		case http.MethodPost:
			h.sendMessage(w, r, parts[0])
		case http.MethodGet:
			h.messageHistory(w, r, parts[0])
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
		}
	case "sync":
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			return
		}
		h.syncMessages(w, r, parts[0])
	default:
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
	}
}

// queryContext adds a bounded timeout to database work while respecting the
// client request cancellation.
func queryContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}
