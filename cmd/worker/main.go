package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloudchat/internal/config"
	"cloudchat/internal/db"
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
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	log.Print("worker process started")

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			checkContext, cancel := context.WithTimeout(ctx, 2*time.Second)
			postgresErr := pool.Ping(checkContext)
			valkeyErr := valkey.Ping(checkContext).Err()
			cancel()
			if postgresErr != nil || valkeyErr != nil {
				log.Print("worker dependency check failed")
				continue
			}
			log.Print("worker heartbeat: dependencies ready")
		}
	}
}
