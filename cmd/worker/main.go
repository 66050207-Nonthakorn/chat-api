package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"cloudchat/internal/config"
	"cloudchat/internal/db"
	"cloudchat/internal/outbox"
)

func main() {
	if err := run(); err != nil {
		log.Printf("worker stopped: %v", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker := outbox.NewWorker(pool, valkey, cfg.Environment)
	return worker.Run(ctx)
}
