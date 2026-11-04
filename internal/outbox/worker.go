package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	randv2 "math/rand/v2"
	"sync"
	"time"

	"cloudchat/internal/config"
	"cloudchat/internal/message"
	"cloudchat/internal/migrations"
	"cloudchat/internal/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	defaultBatchSize      = 20
	defaultConcurrency    = 5
	defaultLeaseDuration  = 30 * time.Second
	defaultPublishTimeout = 5 * time.Second
	defaultRetryBaseDelay = time.Second
	defaultRetryMaxDelay  = 60 * time.Second
	defaultPollInterval   = 250 * time.Millisecond
	defaultShutdownGrace  = 5 * time.Second
	defaultMetricsPeriod  = 30 * time.Second
	retryAlertAttempt     = 10
)

// WorkerOptions controls bounded work and lease/retry timing. Zero values use
// the database schema's v1 defaults.
type WorkerOptions struct {
	BatchSize      int
	Concurrency    int
	LeaseDuration  time.Duration
	PublishTimeout time.Duration
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	PollInterval   time.Duration
	ShutdownGrace  time.Duration
	MetricsPeriod  time.Duration
	Logger         *slog.Logger
}

// BatchResult reports the externally relevant outcome of one claim attempt.
type BatchResult struct {
	Claimed        int
	Published      int
	RetryScheduled int
	LeaseLost      int
}

// Worker claims committed message notifications, publishes them to the
// environment's shared Valkey channel, and fences every state change by lease.
type Worker struct {
	pool    *pgxpool.Pool
	valkey  redis.UniversalClient
	channel string
	options WorkerOptions
	logger  *slog.Logger
}

// NewWorker creates a worker with the agreed v1 batch, lease, timeout, retry,
// and polling defaults.
func NewWorker(pool *pgxpool.Pool, valkey redis.UniversalClient, environment config.Environment) *Worker {
	return NewWorkerWithOptions(pool, valkey, environment, WorkerOptions{})
}

// NewWorkerWithOptions is the configurable public service seam. Smaller
// timings are useful for integration tests that inject lease expiry or retry.
func NewWorkerWithOptions(pool *pgxpool.Pool, valkey redis.UniversalClient, environment config.Environment, options WorkerOptions) *Worker {
	options = normalizeOptions(options)
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		pool:    pool,
		valkey:  valkey,
		channel: EventChannel(string(environment)),
		options: options,
		logger:  logger,
	}
}

// EventChannel returns the single Pub/Sub channel shared by all replicas in
// one environment.
func EventChannel(environment string) string {
	return "cloudchat:" + environment + ":events"
}

func normalizeOptions(options WorkerOptions) WorkerOptions {
	if options.BatchSize <= 0 {
		options.BatchSize = defaultBatchSize
	}
	if options.Concurrency <= 0 {
		options.Concurrency = defaultConcurrency
	}
	if options.Concurrency > options.BatchSize {
		options.Concurrency = options.BatchSize
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultLeaseDuration
	}
	if options.PublishTimeout <= 0 {
		options.PublishTimeout = defaultPublishTimeout
	}
	if options.RetryBaseDelay <= 0 {
		options.RetryBaseDelay = defaultRetryBaseDelay
	}
	if options.RetryMaxDelay <= 0 {
		options.RetryMaxDelay = defaultRetryMaxDelay
	}
	if options.RetryMaxDelay < options.RetryBaseDelay {
		options.RetryMaxDelay = options.RetryBaseDelay
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.ShutdownGrace <= 0 {
		options.ShutdownGrace = defaultShutdownGrace
	}
	if options.MetricsPeriod <= 0 {
		options.MetricsPeriod = defaultMetricsPeriod
	}
	return options
}

// Run drains due batches and waits between empty polls. Cancellation stops new
// claims; any in-flight lease remains recoverable after it expires.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	w.logger.Info("outbox worker started", "event", "worker.started", "service", "worker")
	lastMetrics := time.Time{}
	for {
		if lastMetrics.IsZero() || time.Since(lastMetrics) >= w.options.MetricsPeriod {
			w.reportBacklog(ctx)
			lastMetrics = time.Now()
		}
		result, err := w.ProcessBatch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				w.logger.Info("outbox worker stopped", "event", "worker.stopped", "service", "worker")
				return nil
			}
			w.logger.Error("outbox batch failed", "event", "dependency.health", "service", "worker",
				"dependency", "postgres_or_valkey", "state", "unavailable", "error_code", "OUTBOX_BATCH_FAILED")
		} else if result.Claimed > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			w.logger.Info("outbox worker stopped", "event", "worker.stopped", "service", "worker")
			return nil
		case <-ticker.C:
		}
	}
}

// ProcessBatch claims a bounded set of due rows, commits those leases, then
// publishes outside the PostgreSQL transaction.
func (w *Worker) ProcessBatch(ctx context.Context) (BatchResult, error) {
	claimed, token, err := w.claimBatch(ctx)
	if err != nil {
		return BatchResult{}, err
	}
	result := BatchResult{Claimed: len(claimed)}
	if len(claimed) == 0 {
		return result, nil
	}

	results := make(chan itemResult, len(claimed))
	semaphore := make(chan struct{}, w.options.Concurrency)
	var wait sync.WaitGroup
	for _, item := range claimed {
		item := item
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			results <- itemResult{kind: itemLeaseLost}
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer func() { <-semaphore }()
			results <- w.processItem(ctx, item, token)
		}()
	}
	wait.Wait()
	close(results)

	for processed := range results {
		switch processed.kind {
		case itemPublished:
			result.Published++
		case itemRetried:
			result.RetryScheduled++
		case itemLeaseLost:
			result.LeaseLost++
		}
		if processed.err != nil && err == nil {
			err = processed.err
		}
	}
	if ctx.Err() != nil {
		releaseContext, cancel := context.WithTimeout(context.Background(), w.options.ShutdownGrace)
		released, releaseErr := w.releaseClaimed(releaseContext, token)
		cancel()
		if releaseErr != nil {
			w.logger.Error("outbox shutdown lease release failed", "event", "dependency.health", "service", "worker",
				"dependency", "postgres", "state", "unavailable", "error_code", "OUTBOX_SHUTDOWN_RELEASE_FAILED")
			if err == nil {
				err = fmt.Errorf("release outbox leases during shutdown: %w", releaseErr)
			}
		} else {
			w.logger.Info("outbox shutdown leases released", "event", "outbox.shutdown", "service", "worker", "released_count", released)
		}
	}
	return result, err
}

func (w *Worker) releaseClaimed(ctx context.Context, token string) (int64, error) {
	tag, err := w.pool.Exec(ctx, `
		UPDATE message_outbox
		SET available_at = clock_timestamp(), lease_token = NULL, lease_until = NULL
		WHERE lease_token = $1::uuid AND published_at IS NULL`, token)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (w *Worker) reportBacklog(ctx context.Context) {
	var pending int64
	var oldestAge float64
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - min(created_at))), 0)::float8
		FROM message_outbox WHERE published_at IS NULL`).Scan(&pending, &oldestAge); err != nil {
		if ctx.Err() == nil {
			w.logger.Warn("outbox backlog metric unavailable", "event", "dependency.health", "service", "worker",
				"dependency", "postgres", "state", "unavailable", "error_code", "OUTBOX_METRIC_QUERY_FAILED")
		}
		return
	}
	w.logger.Info("outbox backlog health", "event", "outbox.health", "service", "worker",
		"pending_count", pending, "oldest_age_seconds", oldestAge)
}

type claimedItem struct {
	outboxID   string
	messageID  string
	eventType  string
	schema     int16
	attempt    int
	roomID     string
	sequence   string
	senderID   string
	senderName string
	clientID   string
	text       string
	createdAt  time.Time
	image      *message.Image
}

type itemOutcome uint8

const (
	itemPublished itemOutcome = iota + 1
	itemRetried
	itemLeaseLost
)

type itemResult struct {
	kind itemOutcome
	err  error
}

func (w *Worker) claimBatch(ctx context.Context) ([]claimedItem, string, error) {
	token, err := uuid.New()
	if err != nil {
		return nil, "", fmt.Errorf("create outbox lease token: %w", err)
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	imageJoin, err := migrations.MessageImageJoin(ctx, tx)
	if err != nil {
		return nil, "", fmt.Errorf("check outbox schema: %w", err)
	}
	rows, err := tx.Query(ctx, `
		WITH picked AS (
			SELECT outbox_id
			FROM message_outbox
			WHERE published_at IS NULL
			  AND available_at <= clock_timestamp()
			  AND (lease_until IS NULL OR lease_until <= clock_timestamp())
			ORDER BY available_at, outbox_id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		), claimed AS (
			UPDATE message_outbox AS outbox
			SET lease_token = $2::uuid,
			    lease_until = clock_timestamp() + ($3::bigint * interval '1 microsecond'),
			    attempt_count = outbox.attempt_count + 1
			FROM picked
			WHERE outbox.outbox_id = picked.outbox_id
			RETURNING outbox.outbox_id, outbox.message_id, outbox.event_type,
			          outbox.schema_version, outbox.attempt_count
		)
		SELECT claimed.outbox_id::text, claimed.message_id::text,
		       claimed.event_type, claimed.schema_version, claimed.attempt_count,
		       m.room_id::text, m.seq::text, m.sender_user_id::text,
		       m.sender_display_name, m.client_message_id::text,
		       m.text, m.created_at, images.image_id::text,
	       images.content_type, images.byte_size, images.width, images.height
		FROM claimed
		JOIN messages m ON m.message_id = claimed.message_id `+imageJoin+`
		ORDER BY claimed.outbox_id`,
		w.options.BatchSize, token, w.options.LeaseDuration.Microseconds())
	if err != nil {
		return nil, "", fmt.Errorf("claim outbox batch: %w", err)
	}
	items := make([]claimedItem, 0, w.options.BatchSize)
	for rows.Next() {
		var item claimedItem
		var imageID, contentType pgtype.Text
		var byteSize, width, height pgtype.Int4
		if err := rows.Scan(
			&item.outboxID, &item.messageID, &item.eventType, &item.schema,
			&item.attempt, &item.roomID, &item.sequence, &item.senderID,
			&item.senderName, &item.clientID, &item.text, &item.createdAt,
			&imageID, &contentType, &byteSize, &width, &height,
		); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("read claimed outbox row: %w", err)
		}
		if imageID.Valid {
			item.image = &message.Image{
				URL:         "/api/v1/rooms/" + item.roomID + "/messages/" + item.messageID + "/image",
				ContentType: contentType.String,
				ByteSize:    int(byteSize.Int32),
				Width:       int(width.Int32),
				Height:      int(height.Int32),
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", fmt.Errorf("read claimed outbox rows: %w", err)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, "", fmt.Errorf("commit outbox claim: %w", err)
	}
	return items, token, nil
}

func (w *Worker) processItem(ctx context.Context, item claimedItem, token string) itemResult {
	if item.eventType != message.CreatedType || item.schema != 1 {
		return w.retryItem(ctx, item, token, "UNSUPPORTED_EVENT_SCHEMA")
	}
	valid, err := w.renewLease(ctx, item.outboxID, token)
	if err != nil {
		return itemResult{err: fmt.Errorf("renew outbox lease: %w", err)}
	}
	if !valid {
		return itemResult{kind: itemLeaseLost}
	}

	payload, err := json.Marshal(message.CreatedEvent{
		Type: message.CreatedType,
		Message: message.Message{
			MessageID:       item.messageID,
			RoomID:          item.roomID,
			Seq:             item.sequence,
			Sender:          message.Sender{UserID: item.senderID, DisplayName: item.senderName},
			ClientMessageID: item.clientID,
			Text:            item.text,
			Image:           item.image,
			CreatedAt:       item.createdAt.UTC().Format(time.RFC3339Nano),
		},
	})
	if err != nil {
		return w.retryItem(ctx, item, token, "EVENT_SERIALIZATION_FAILED")
	}

	publishCtx, cancel := context.WithTimeout(ctx, w.options.PublishTimeout)
	err = w.valkey.Publish(publishCtx, w.channel, string(payload)).Err()
	cancel()
	if err != nil {
		return w.retryItem(ctx, item, token, "VALKEY_PUBLISH_FAILED")
	}

	marked, err := w.markPublished(ctx, item.outboxID, token)
	if err != nil {
		return itemResult{err: fmt.Errorf("mark outbox published: %w", err)}
	}
	if !marked {
		return itemResult{kind: itemLeaseLost}
	}
	return itemResult{kind: itemPublished}
}

func (w *Worker) renewLease(ctx context.Context, outboxID, token string) (bool, error) {
	tag, err := w.pool.Exec(ctx, `
		UPDATE message_outbox
		SET lease_until = clock_timestamp() + ($3::bigint * interval '1 microsecond')
		WHERE outbox_id = $1::uuid AND lease_token = $2::uuid
		  AND published_at IS NULL AND lease_until > clock_timestamp()`,
		outboxID, token, w.options.LeaseDuration.Microseconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (w *Worker) markPublished(ctx context.Context, outboxID, token string) (bool, error) {
	tag, err := w.pool.Exec(ctx, `
		UPDATE message_outbox
		SET published_at = clock_timestamp(), lease_token = NULL, lease_until = NULL,
		    last_error_code = NULL
		WHERE outbox_id = $1::uuid AND lease_token = $2::uuid
		  AND published_at IS NULL AND lease_until > clock_timestamp()`,
		outboxID, token)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (w *Worker) retryItem(ctx context.Context, item claimedItem, token, errorCode string) itemResult {
	delay := w.retryDelay(item.attempt)
	tag, err := w.pool.Exec(ctx, `
		UPDATE message_outbox
		SET available_at = clock_timestamp() + ($4::bigint * interval '1 microsecond'),
		    lease_token = NULL, lease_until = NULL, last_error_code = $3
		WHERE outbox_id = $1::uuid AND lease_token = $2::uuid
		  AND published_at IS NULL AND lease_until > clock_timestamp()`,
		item.outboxID, token, errorCode, delay.Microseconds())
	if err != nil {
		return itemResult{err: fmt.Errorf("schedule outbox retry: %w", err)}
	}
	if tag.RowsAffected() != 1 {
		return itemResult{kind: itemLeaseLost}
	}
	if item.attempt > retryAlertAttempt {
		w.logger.Error("outbox retry threshold exceeded", "event", "outbox.retry", "service", "worker",
			"alert", "OUTBOX_RETRY_THRESHOLD_EXCEEDED", "attempt_count", item.attempt, "error_code", errorCode)
	} else {
		w.logger.Warn("outbox retry scheduled", "event", "outbox.retry", "service", "worker",
			"attempt_count", item.attempt, "error_code", errorCode)
	}
	return itemResult{kind: itemRetried}
}

func (w *Worker) retryDelay(attempt int) time.Duration {
	cap := w.options.RetryBaseDelay
	for retry := 1; retry < attempt; retry++ {
		if cap >= w.options.RetryMaxDelay/2 {
			cap = w.options.RetryMaxDelay
			break
		}
		cap *= 2
	}
	if cap > w.options.RetryMaxDelay {
		cap = w.options.RetryMaxDelay
	}
	if cap <= 0 {
		return 0
	}
	return time.Duration(randv2.Int64N(int64(cap) + 1))
}
