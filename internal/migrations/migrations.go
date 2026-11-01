package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const CurrentVersion = 1

const advisoryLockKey int64 = 1380207954

//go:embed sql/*.sql
var migrationFiles embed.FS

func Up(ctx context.Context, pool *pgxpool.Pool) error {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("could not acquire PostgreSQL connection")
	}
	defer connection.Release()

	if _, err = connection.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("could not acquire migration lock")
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockContext, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	_, err = connection.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
		)`)
	if err != nil {
		return fmt.Errorf("could not initialize migration metadata")
	}

	var currentVersion int
	if err = connection.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion); err != nil {
		return fmt.Errorf("could not read migration version")
	}
	if currentVersion > CurrentVersion {
		return fmt.Errorf("database schema version %d is newer than this backend supports", currentVersion)
	}
	if currentVersion == CurrentVersion {
		return nil
	}

	migration, err := fs.ReadFile(migrationFiles, "sql/0001_initial.sql")
	if err != nil {
		return fmt.Errorf("could not read initial migration")
	}
	transaction, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("could not begin migration transaction")
	}
	defer func() { _ = transaction.Rollback(context.Background()) }()

	if _, err = transaction.Exec(ctx, string(migration), pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("initial migration failed")
	}
	if _, err = transaction.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", CurrentVersion); err != nil {
		return fmt.Errorf("could not record initial migration")
	}
	if err = transaction.Commit(ctx); err != nil {
		return fmt.Errorf("could not commit initial migration")
	}
	return nil
}
