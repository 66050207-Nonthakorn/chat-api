package migrations

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Version(ctx context.Context, q queryer) (int, error) {
	var version int
	err := q.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	return version, err
}

// CheckVersion is explicit: schema 1 supports text during the compatibility
// rollout; schemas 2 and 3 also support images. Unknown future schemas fail closed.
func CheckVersion(version int, imageSends bool) error {
	if version < 1 || version > CurrentVersion || (imageSends && version < 2) {
		return fmt.Errorf("unsupported database schema %d", version)
	}
	return nil
}

// MessageImageJoin keeps the same result columns before migration 0002 exists.
// All callers use the fixed messages alias m. Recheck each operation so running
// API/worker tasks pick up the additive migration without restarting.
func MessageImageJoin(ctx context.Context, q queryer) (string, error) {
	version, err := Version(ctx, q)
	if err != nil {
		return "", err
	}
	if err := CheckVersion(version, false); err != nil {
		return "", err
	}
	if version == 1 {
		return ` LEFT JOIN (SELECT NULL::uuid image_id, NULL::text content_type,
			NULL::integer byte_size, NULL::integer width, NULL::integer height) images ON false `, nil
	}
	return ` LEFT JOIN image_upload_candidates images ON images.image_id = m.image_id `, nil
}
