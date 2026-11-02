// Package testutil provides shared integration-test fixtures.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"cloudchat/internal/migrations"
	"cloudchat/internal/seed"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IsolatedDatabase creates a migrated, seeded schema and cleans it up after the test.
func IsolatedDatabase(t testing.TB, databaseURL, schemaPrefix string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	t.Cleanup(admin.Close)

	var schemaBytes [8]byte
	if _, err := rand.Read(schemaBytes[:]); err != nil {
		t.Fatalf("create isolated schema name: %v", err)
	}
	schema := schemaPrefix + hex.EncodeToString(schemaBytes[:])
	schemaSQL := (pgx.Identifier{schema}).Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schemaSQL); err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schemaSQL+" CASCADE") })

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test PostgreSQL URL: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("connect to isolated schema: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatalf("apply migrations in isolated schema: %v", err)
	}
	if err := seed.Apply(ctx, pool); err != nil {
		t.Fatalf("seed isolated fixtures: %v", err)
	}
	return pool
}
