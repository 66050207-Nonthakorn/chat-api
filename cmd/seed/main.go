package main

import (
	"context"
	"log"
	"os"
	"time"

	"cloudchat/internal/config"
	"cloudchat/internal/db"
	"cloudchat/internal/seed"
)

func main() {
	if err := run(); err != nil {
		log.Printf("seed failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.OpenPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return seed.Apply(ctx, pool)
}
