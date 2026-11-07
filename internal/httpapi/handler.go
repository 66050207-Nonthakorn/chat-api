package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"cloudchat/internal/apierror"
	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/health"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/uuid"
	"github.com/gin-gonic/gin"
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
			api.routeNotFound(w, r)
			return
		}
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
	})
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	// Preserve existing errors for unsupported methods and unknown paths.
	router.NoRoute(gin.WrapH(root))
	router.GET("/health/live", gin.WrapH(healthHandler))
	router.GET("/health/ready", gin.WrapH(healthHandler))
	router.HEAD("/health/live", gin.WrapH(healthHandler))
	router.HEAD("/health/ready", gin.WrapH(healthHandler))
	router.GET("/api/v1/ws", gin.WrapF(api.serveWebSocket))
	router.GET("/api/v1/rooms", api.route(api.listRooms))
	router.POST("/api/v1/rooms", api.route(api.createRoom))
	router.GET("/api/v1/users", api.route(api.searchUsers))
	router.GET("/api/v1/rooms/:roomID/members", api.roomRoute(api.listMembers))
	router.POST("/api/v1/rooms/:roomID/members", api.roomRoute(api.addMembers))
	router.GET("/api/v1/rooms/:roomID/messages", api.roomRoute(api.messageHistory))
	router.POST("/api/v1/rooms/:roomID/messages", api.roomRoute(api.sendMessage))
	router.GET("/api/v1/rooms/:roomID/sync", api.roomRoute(api.syncMessages))
	router.GET("/api/v1/rooms/:roomID/messages/:messageID/image", func(c *gin.Context) {
		if _, ok := requirePrincipal(c.Writer, c.Request); !ok {
			return
		}
		roomID, messageID := c.Param("roomID"), c.Param("messageID")
		if !uuid.IsCanonical(roomID) || !uuid.IsCanonical(messageID) {
			writeError(c.Writer, c.Request, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
			return
		}
		api.readMessageImage(c.Writer, c.Request, roomID, messageID)
	})
	var authenticated http.Handler = router
	if api.rateLimit != nil || cfg.Environment != config.EnvironmentLocal {
		authenticated = api.rateLimitMiddleware(authenticated)
	}
	if api.auth != nil {
		authenticated = api.auth.Middleware(authenticated)
	}
	return requestMetadata(auth.LocalTestIdentityMiddleware(cfg, authenticated), logger)
}

func (h *handler) route(handle func(http.ResponseWriter, *http.Request)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := requirePrincipal(c.Writer, c.Request); ok {
			handle(c.Writer, c.Request)
		}
	}
}

func (h *handler) roomRoute(handle func(http.ResponseWriter, *http.Request, string)) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := requirePrincipal(c.Writer, c.Request); !ok {
			return
		}
		roomID := c.Param("roomID")
		if !uuid.IsCanonical(roomID) {
			writeError(c.Writer, c.Request, http.StatusNotFound, "ROOM_NOT_FOUND", "Room not found", false, nil)
			return
		}
		handle(c.Writer, c.Request, roomID)
	}
}

func (h *handler) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.websocket == nil {
		dependencyUnavailable(w, r)
		return
	}
	h.websocket.ServeHTTP(w, r)
}

func (h *handler) routeNotFound(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v1/ws", "/api/v1/rooms", "/api/v1/users":
		writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/rooms/") {
		if _, ok := requirePrincipal(w, r); !ok {
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/rooms/"), "/")
		if len(parts) == 4 && parts[1] == "messages" && parts[3] == "image" {
			if uuid.IsCanonical(parts[0]) && uuid.IsCanonical(parts[2]) {
				writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			} else {
				writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found", false, nil)
			}
			return
		}
		if len(parts) != 2 || !uuid.IsCanonical(parts[0]) {
			writeRoomNotFound(w, r)
			return
		}
		if parts[1] == "members" || parts[1] == "messages" || parts[1] == "sync" {
			writeError(w, r, http.StatusMethodNotAllowed, "INVALID_REQUEST", "Method not allowed", false, nil)
			return
		}
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

func dependencyUnavailable(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required service is unavailable", true, nil)
}

// queryContext adds a bounded timeout to database work while respecting the
// client request cancellation.
func queryContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}
