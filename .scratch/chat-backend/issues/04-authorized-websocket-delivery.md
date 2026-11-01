# 04: Stream authorized messages over WebSocket

**Type:** task

**What to build:** A connected client can subscribe to an authorized room and receive committed messages from any API replica, with bounded resource use and a clear recovery path when live delivery is missed.

**Blocked by:** 03: Publish outbox events with recoverable leases.

**Status:** resolved

- [x] The endpoint negotiates `chat.v1`, checks session and Origin before upgrade, and implements subscribe, unsubscribe, and ping/pong frames.
- [x] Subscription authorization is checked against current PostgreSQL membership; acknowledgement is queued before subsequent room events.
- [x] Pub/Sub events fan out across API replicas; duplicate, missing, and out-of-order notifications do not change durable HTTP history.
- [x] Session and membership are rechecked before private sends; access revocation or unavailable authorization stops further delivery.
- [x] Per-socket subscriptions and outbound queues are bounded; full queues close with the specified recovery code.
- [x] WebSocket protocol tests use real clients and verify cross-replica delivery, authorization, reconnect, and recovery via HTTP sync.

## Answer

Implemented in `9e254a4` and `48ced56`, then merged into `integration` at `48ced56`.

Verification: public-client integration tests, uncached `go test -count=1 ./...`, `go vet ./...`, `go build ./cmd/...`, and focused race tests passed against PostgreSQL/Valkey Compose services. Coverage includes handshake identity/Origin/subprotocol, authorization and revocation, fanout, missed-event recovery, ping/unsubscribe, subscription cap, queue overload, and oversized frames. Shared ticket03 services remain healthy.
