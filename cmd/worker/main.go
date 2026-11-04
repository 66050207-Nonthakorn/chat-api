package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"cloudchat/internal/config"
	"cloudchat/internal/db"
	"cloudchat/internal/imagecleanup"
	"cloudchat/internal/imagestore"
	"cloudchat/internal/outbox"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("worker stopped", "event", "process.stopped", "service", "worker", "error_code", "WORKER_STOPPED")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
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
	imageStore, err := imagestore.Open(context.Background(), cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go imagecleanup.Run(ctx, pool, imageStore)
	worker := outbox.NewWorkerWithOptions(pool, valkey, cfg.Environment, outbox.WorkerOptions{
		ShutdownGrace: cfg.ShutdownGrace,
	})
	return worker.Run(ctx)
}
