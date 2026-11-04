package imagecleanup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloudchat/internal/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

const batchSize = 50

type imageDeleter interface {
	Delete(context.Context, string) error
}

func Run(ctx context.Context, pool *pgxpool.Pool, store imageDeleter) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := ProcessBatch(ctx, pool, store); err != nil && ctx.Err() == nil {
			slog.Warn("image cleanup batch failed", "event", "image.cleanup", "service", "worker", "error_code", "IMAGE_CLEANUP_FAILED")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func ProcessBatch(ctx context.Context, pool *pgxpool.Pool, store imageDeleter) error {
	version, err := migrations.Version(ctx, pool)
	if err != nil {
		return err
	}
	if err := migrations.CheckVersion(version, false); err != nil {
		return err
	}
	if version < 2 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin image cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT image_id
			FROM image_upload_candidates
			WHERE (state = 'candidate' AND created_at <= clock_timestamp() - interval '24 hours')
			   OR (state = 'deleting' AND (cleanup_after IS NULL OR cleanup_after <= clock_timestamp()))
			ORDER BY created_at, image_id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE image_upload_candidates AS images
		SET state = 'deleting', cleanup_after = clock_timestamp() + interval '5 minutes'
		FROM picked WHERE images.image_id = picked.image_id
		RETURNING images.image_id::text, images.object_key, images.object_deleted_at IS NOT NULL`, batchSize)
	if err != nil {
		return fmt.Errorf("claim image cleanup: %w", err)
	}
	type object struct {
		id, key   string
		tombstone bool
	}
	objects := make([]object, 0, batchSize)
	for rows.Next() {
		var item object
		if err := rows.Scan(&item.id, &item.key, &item.tombstone); err != nil {
			rows.Close()
			return fmt.Errorf("read image cleanup candidate: %w", err)
		}
		objects = append(objects, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read image cleanup candidates: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit image cleanup claim: %w", err)
	}

	for _, item := range objects {
		deleteCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := store.Delete(deleteCtx, item.key)
		cancel()
		if err != nil {
			_, _ = pool.Exec(ctx, `UPDATE image_upload_candidates SET cleanup_after = clock_timestamp() + interval '5 minutes' WHERE image_id = $1::uuid AND state = 'deleting'`, item.id)
			continue
		}
		if item.tombstone {
			_, _ = pool.Exec(ctx, `DELETE FROM image_upload_candidates images WHERE images.image_id = $1::uuid AND images.state = 'deleting' AND images.object_deleted_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM messages WHERE messages.image_id = images.image_id)`, item.id)
			continue
		}
		_, _ = pool.Exec(ctx, `UPDATE image_upload_candidates SET object_deleted_at = clock_timestamp(), cleanup_after = clock_timestamp() + interval '24 hours' WHERE image_id = $1::uuid AND state = 'deleting' AND object_deleted_at IS NULL`, item.id)
	}
	return nil
}
