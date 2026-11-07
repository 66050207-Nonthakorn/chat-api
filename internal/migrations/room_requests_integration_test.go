package migrations_test

import (
	"context"
	"os"
	"testing"

	"cloudchat/internal/demo"
	"cloudchat/internal/migrations"
	"cloudchat/internal/testutil"
)

func TestRoomRequestsMigrationFromSchemaTwo(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to test migrations with PostgreSQL")
	}
	pool := testutil.IsolatedDatabase(t, databaseURL, "room_migration_")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DROP TABLE room_requests; DELETE FROM schema_migrations WHERE version = 3`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := migrations.Up(ctx, pool); err != nil {
			t.Fatalf("migration attempt %d: %v", i+1, err)
		}
	}
	version, err := migrations.Version(ctx, pool)
	if err != nil || version != 3 {
		t.Fatalf("schema version = %d, err = %v", version, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM room_memberships WHERE room_id=$1::uuid`, demo.GroupRoomID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("seeded memberships after migration = %d, err = %v", count, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO room_requests(user_id, operation, request_id, request_hash, room_id) VALUES ($1::uuid, 'create_group', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', decode(repeat('00',32),'hex'), $2::uuid)`, demo.UserAID, demo.GroupRoomID); err != nil {
		t.Fatalf("insert migration record: %v", err)
	}
}
