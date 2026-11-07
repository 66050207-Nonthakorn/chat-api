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

const CurrentVersion = 3

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
	for version := currentVersion + 1; version <= CurrentVersion; version++ {
		matches, err := fs.Glob(migrationFiles, fmt.Sprintf("sql/%04d_*.sql", version))
		if err != nil || len(matches) != 1 {
			return fmt.Errorf("could not find migration %04d", version)
		}
		migration, err := fs.ReadFile(migrationFiles, matches[0])
		if err != nil {
			return fmt.Errorf("could not read migration %04d", version)
		}
		transaction, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("could not begin migration %04d", version)
		}
		if _, err = transaction.Exec(ctx, string(migration), pgx.QueryExecModeSimpleProtocol); err != nil {
			_ = transaction.Rollback(context.Background())
			return fmt.Errorf("migration %04d failed", version)
		}
		if _, err = transaction.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", version); err != nil {
			_ = transaction.Rollback(context.Background())
			return fmt.Errorf("could not record migration %04d", version)
		}
		if err := transaction.Commit(ctx); err != nil {
			return fmt.Errorf("could not commit migration %04d", version)
		}
	}
	return nil
}
