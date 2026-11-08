package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"cloudchat/internal/apierror"
	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/health"
	"cloudchat/internal/ratelimit"
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
	app := api.routes(health.NewHandler(options.Readiness))
	if api.rateLimit != nil || cfg.Environment != config.EnvironmentLocal {
		app = api.rateLimitMiddleware(app)
	}
	if api.auth != nil {
		app = api.auth.Middleware(app)
	}
	app = auth.LocalTestIdentityMiddleware(cfg, app)
	return requestMetadata(app, logger)
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
