package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloudchat/internal/auth"
	"cloudchat/internal/config"
	"cloudchat/internal/db"
	"cloudchat/internal/health"
	"cloudchat/internal/migrations"
)

func main() {
	if err := run(); err != nil {
		log.Printf("api stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := db.OpenPostgres(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	valkey, err := db.OpenValkey(cfg.ValkeyURL)
	if err != nil {
		return err
	}
	defer func() { _ = valkey.Close() }()

	readiness := func(parent context.Context) error {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			return err
		}

		var schemaVersion int
		if err := pool.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&schemaVersion); err != nil {
			return err
		}
		if schemaVersion != migrations.CurrentVersion {
			return errors.New("database schema is not supported")
		}

		subscription := valkey.Subscribe(ctx, "health:readiness-probe")
		defer func() { _ = subscription.Close() }()
		_, err := subscription.Receive(ctx)
		return err
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           auth.LocalTestIdentityMiddleware(cfg, health.NewHandler(readiness)),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	}
}
