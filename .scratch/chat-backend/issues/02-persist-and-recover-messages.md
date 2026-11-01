# 02: Send and recover persisted messages over HTTP

**What to build:** An authorized user can list their rooms, send a message safely across retries, and page through history or a bounded sync snapshot after reconnecting.

**Blocked by:** 01: Run a local backend foundation.

**Status:** ready-for-agent

- [ ] Room listing, send, history, and sync follow the v1 HTTP contract and common error envelope.
- [ ] Current membership is checked before reads, new sends, and idempotent replay; unavailable and unauthorized rooms return the same 404.
- [ ] A transaction allocates contiguous per-room sequence numbers and inserts the immutable message and outbox row together.
- [ ] Concurrent retries with the same sender, room, client message ID, and text return one logical message; different text returns 409.
- [ ] Rollback leaves neither a message nor an outbox event and does not consume a sequence number.
- [ ] History and snapshot sync pagination preserve order, reject invalid cursors, and do not skip gaps.
- [ ] HTTP integration tests exercise the public endpoints against PostgreSQL, including 100 concurrent retries and retry after an unknown response outcome.
