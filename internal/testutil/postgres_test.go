package testutil_test

import (
	"context"
	"os"
	"testing"

	"cloudchat/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIsolatedDatabaseSeedsAndCleansSchema(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL fixture tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	defer admin.Close()

	var schema string
	if !t.Run("fixture", func(t *testing.T) {
		pool := testutil.IsolatedDatabase(t, databaseURL, "fixture_test_")
		if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
			t.Fatalf("read fixture schema: %v", err)
		}
		var users int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM app_users").Scan(&users); err != nil || users == 0 {
			t.Fatalf("seeded users = %d, error = %v", users, err)
		}
	}) {
		t.Fatal("fixture setup failed")
	}
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists); err != nil {
		t.Fatalf("check schema cleanup: %v", err)
	}
	if exists {
		t.Fatalf("fixture schema %q was not removed", schema)
	}
}
