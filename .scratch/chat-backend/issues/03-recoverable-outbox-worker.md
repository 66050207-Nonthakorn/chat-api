# 03: Publish outbox events with recoverable leases

**What to build:** A committed message is eventually published by a separate worker, and a replacement worker can recover work after a crash without a stale worker overwriting the new lease owner.

**Blocked by:** 02: Send and recover persisted messages over HTTP.

**Status:** ready-for-agent

- [ ] The worker claims due outbox rows in bounded batches using leases and `SKIP LOCKED`.
- [ ] A fresh fencing token guards lease renewal, publish completion, and retry scheduling.
- [ ] Worker crashes before publish and after publish-before-mark are recoverable; duplicate publication is allowed and documented.
- [ ] Publish uses the shared Valkey channel/prefix for the environment and emits the v1 `message.created` event.
- [ ] Retry uses bounded concurrency and full jitter; repeated failures remain pending and produce a safe error code/alert signal.
- [ ] Worker tests exercise PostgreSQL and Valkey at their public service boundaries and verify effects through the message/live-delivery contract.
