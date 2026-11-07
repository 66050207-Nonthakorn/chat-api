package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"cloudchat/internal/apierror"
	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/health"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/requestid"
	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type handler struct {
	config           config.Config
	pool             *pgxpool.Pool
	websocket        http.Handler
	auth             *auth.Server
	rateLimit        *ratelimit.Limiter
	images           imageStore
	imageUploadSlots chan struct{}
}

// Options supplies HTTP dependencies. The caller owns their
// process lifecycles and shares the rate limiter with its realtime server.
type Options struct {
	Readiness   health.ReadinessCheck
	WebSocket   http.Handler
	Auth        *auth.Server
	RateLimiter *ratelimit.Limiter
	Images      imageStore
	// Logger defaults to slog.Default at construction when omitted.
	Logger *slog.Logger
	// ImageUploadSlots must be one capacity-one channel shared by all handlers
	// in an API process. Image sends fail closed when it is omitted.
	ImageUploadSlots chan struct{}
}

// NewHandler wires the public HTTP API, authentication and health endpoints.
// Missing dependencies retain their endpoint-specific unavailable responses;
// authenticated rate-limited requests fail closed outside local mode.
func NewHandler(cfg config.Config, pool *pgxpool.Pool, options Options) http.Handler {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	api := &handler{
		config: cfg, pool: pool,
		websocket:        options.WebSocket,
		auth:             options.Auth,
		rateLimit:        options.RateLimiter,
		images:           options.Images,
		imageUploadSlots: options.ImageUploadSlots,
	}
	healthHandler := health.NewHandler(options.Readiness)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" {
			healthHandler.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") || r.URL.Path == "/api/v1/session" {
			if api.auth == nil {
				dependencyUnavailable(w, r)
				return
			}
			api.auth.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.serveHTTP(w, r)
			return
		}
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
	})
	var authenticated http.Handler = root
	if api.rateLimit != nil || cfg.Environment != config.EnvironmentLocal {
		authenticated = api.rateLimitMiddleware(authenticated)
	}
	if api.auth != nil {
		authenticated = api.auth.Middleware(authenticated)
	}
	return requestMetadata(auth.LocalTestIdentityMiddleware(cfg, authenticated), logger)
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
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			return
		}
		if _, ok := requirePrincipal(w, r); !ok {
			return
		}
		if r.Method == http.MethodPost {
			h.createRoom(w, r)
		} else {
			h.listRooms(w, r)
		}
		return
	}
	if r.URL.Path == "/api/v1/users" {
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			return
		}
		if _, ok := requirePrincipal(w, r); !ok {
			return
		}
		h.searchUsers(w, r)
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

func requestMetadata(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		r = r.WithContext(requestid.WithContext(r.Context(), id))
		observed := &observedResponseWriter{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(observed, r)
		if observed.status == 0 {
			observed.status = http.StatusOK
		}
		if responseID := w.Header().Get("X-Request-ID"); responseID != "" {
			id = responseID
		}
		logger.Info("http request",
			"event", "http.request",
			"service", "api",
			"request_id", id,
			"method", r.Method,
			"route", safeRouteName(r.URL.Path),
			"status", observed.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type observedResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *observedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *observedResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *observedResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *observedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support connection hijacking")
	}
	w.status = http.StatusSwitchingProtocols
	return hijacker.Hijack()
}

func (w *observedResponseWriter) Push(target string, options *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (w *observedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func safeRouteName(path string) string {
	switch {
	case path == "/health/live":
		return "health.live"
	case path == "/health/ready":
		return "health.ready"
	case path == "/auth/callback":
		return "auth.callback"
	case strings.HasPrefix(path, "/auth/"):
		return "auth.endpoint"
	case path == "/api/v1/ws":
		return "api.websocket"
	case strings.Contains(path, "/messages/") && strings.HasSuffix(path, "/image"):
		return "api.message.image"
	case path == "/api/v1/rooms":
		return "api.rooms"
	case path == "/api/v1/users":
		return "api.users"
	case strings.HasPrefix(path, "/api/v1/rooms/") && strings.HasSuffix(path, "/members"):
		return "api.room.members"
	case strings.HasPrefix(path, "/api/v1/rooms/") && strings.HasSuffix(path, "/sync"):
		return "api.room.sync"
	case strings.HasPrefix(path, "/api/v1/rooms/") && strings.HasSuffix(path, "/messages"):
		return "api.room.messages"
	default:
		return "not_found"
	}
}

var fallbackRequestSequence atomic.Uint64

func requestID() string {
	if id, err := requestid.New(); err == nil {
		return id
	}
	return fmt.Sprintf("%d-%d", time.Now().UTC().UnixNano(), fallbackRequestSequence.Add(1))
}

func dependencyUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
}

func (h *handler) serveRoom(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/rooms/")
	parts := strings.Split(path, "/")
	if len(parts) == 4 && parts[1] == "messages" && parts[3] == "image" {
		if !uuid.IsCanonical(parts[0]) || !uuid.IsCanonical(parts[2]) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			return
		}
		h.readMessageImage(w, r, parts[0], parts[2])
		return
	}
	if len(parts) != 2 || !uuid.IsCanonical(parts[0]) {
		writeError(w, r, http.StatusNotFound, "ROOM_NOT_FOUND", "Room not found", false, nil)
		return
	}
	switch parts[1] {
	case "members":
		switch r.Method {
		case http.MethodGet:
			h.listMembers(w, r, parts[0])
		case http.MethodPost:
			h.addMembers(w, r, parts[0])
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
		}
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
