package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"cloudchat/internal/db"
	"cloudchat/internal/migrations"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("migration failed", "event", "migration.failed", "service", "migrate", "error_code", "MIGRATION_FAILED")
		os.Exit(1)
	}
}

func run() error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := db.OpenPostgres(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrations.Up(ctx, pool)
}
