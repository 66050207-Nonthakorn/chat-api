package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/db"
	"cloudchat/internal/health"
	"cloudchat/internal/httpapi"
	"cloudchat/internal/imagestore"
	"cloudchat/internal/migrations"
	"cloudchat/internal/ratelimit"
	"cloudchat/internal/realtime"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("api stopped", "event", "process.stopped", "service", "api", "error_code", "API_STOPPED")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.OpenPostgres(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	sessionRules := auth.NewSessionRules(pool)

	var authServer *auth.Server
	if cfg.OIDCIssuer != "" {
		authServer, err = auth.NewServer(ctx, cfg, pool, sessionRules)
		if err != nil {
			return err
		}
	}

	valkey, err := db.OpenValkey(cfg.ValkeyURL)
	if err != nil {
		return err
	}
	defer func() { _ = valkey.Close() }()
	imageStore, err := imagestore.Open(ctx, cfg)
	if err != nil {
		return err
	}
	rateLimiter, err := ratelimit.New(valkey, string(cfg.Environment))
	if err != nil {
		return err
	}

	realtimeServer, err := realtime.NewServer(context.Background(), pool, valkey, cfg, sessionRules, realtime.Options{Logger: slog.Default(), RateLimiter: rateLimiter})
	if err != nil {
		return err
	}
	defer realtimeServer.Close()
	go monitorRuntimeMetrics(ctx, pool, realtimeServer, slog.Default())

	readiness := func(parent context.Context) error {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			logDependencyHealth("postgres", false, "POSTGRES_UNAVAILABLE")
			return err
		}

		schemaVersion, err := migrations.Version(ctx, pool)
		if err != nil {
			logDependencyHealth("postgres", false, "POSTGRES_SCHEMA_QUERY_FAILED")
			return err
		}
		if err := migrations.CheckVersion(schemaVersion, cfg.ImageMessagesEnabled); err != nil {
			logDependencyHealth("postgres", false, "POSTGRES_SCHEMA_UNSUPPORTED")
			return errors.New("database schema is not supported")
		}
		logDependencyHealth("postgres", true, "")

		subscription := valkey.Subscribe(ctx, "health:readiness-probe")
		defer func() { _ = subscription.Close() }()
		_, err = subscription.Receive(ctx)
		logDependencyHealth("valkey", err == nil, "VALKEY_UNAVAILABLE")
		return err
	}
	drain := health.NewDrainController(readiness)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewHandlerWithAuthAndRateLimiter(cfg, pool, drain.Check, realtimeServer, authServer, rateLimiter, imageStore),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       65 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			serverErrors <- nil
			return
		}
		serverErrors <- err
	}()

	select {
	case err := <-serverErrors:
		return err
	case <-ctx.Done():
		drain.BeginDrain()
		slog.Info("api draining", "event", "service.draining", "service", "api", "grace_seconds", cfg.ShutdownGrace.Seconds())
		shutdownContext, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
		defer cancel()
		httpErr := server.Shutdown(shutdownContext)
		if httpErr != nil {
			_ = server.Close()
		}
		socketErr := realtimeServer.Shutdown(shutdownContext)
		if socketErr != nil {
			return errors.Join(httpErr, socketErr)
		}
		return httpErr
	}
}

func logDependencyHealth(dependency string, healthy bool, errorCode string) {
	state := "healthy"
	if !healthy {
		state = "unavailable"
		slog.Warn("backend dependency unavailable", "event", "dependency.health", "service", "api",
			"dependency", dependency, "state", state, "error_code", errorCode)
		return
	}
	slog.Info("backend dependency healthy", "event", "dependency.health", "service", "api",
		"dependency", dependency, "state", state)
}

func monitorRuntimeMetrics(ctx context.Context, pool *pgxpool.Pool, realtimeServer *realtime.Server, logger *slog.Logger) {
	interval := 30 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		stats := pool.Stat()
		maxConnections := stats.MaxConns()
		inUsePercent := float64(0)
		if maxConnections > 0 {
			inUsePercent = float64(stats.AcquiredConns()) * 100 / float64(maxConnections)
		}
		logger.Info("database pool pressure", "event", "database.pool", "service", "api",
			"pool_total_connections", stats.TotalConns(), "pool_acquired_connections", stats.AcquiredConns(),
			"pool_max_connections", maxConnections, "pool_in_use_percent", inUsePercent)
		realtimeServer.ReportHealth()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
