# 03: Publish outbox events with recoverable leases

**Type:** task

**What to build:** A committed message is eventually published by a separate worker, and a replacement worker can recover work after a crash without a stale worker overwriting the new lease owner.

**Blocked by:** 02: Send and recover persisted messages over HTTP.

**Status:** resolved

- [x] The worker claims due outbox rows in bounded batches using leases and `SKIP LOCKED`.
- [x] A fresh fencing token guards lease renewal, publish completion, and retry scheduling.
- [x] Worker crashes before publish and after publish-before-mark are recoverable; duplicate publication is allowed and documented.
- [x] Publish uses the shared Valkey channel/prefix for the environment and emits the v1 `message.created` event.
- [x] Retry uses bounded concurrency and full jitter; repeated failures remain pending and produce a safe error code/alert signal.
- [x] Worker tests exercise PostgreSQL and Valkey at their public service boundaries and verify effects through the message/live-delivery contract.

## Answer

Implemented in `9aeb27e` and merged into `integration` at `62db25d`.

Verification: `go test -count=1 ./...`, `go vet ./...`, and `go build ./cmd/...` passed against PostgreSQL/Valkey Compose. Integration tests cover HTTP-to-Pub/Sub event payload, abandoned lease recovery, stale-owner fencing, and duplicate publication after a lost acknowledgement; the failure tests passed three consecutive runs.
