# 04: Stream authorized messages over WebSocket

**What to build:** A connected client can subscribe to an authorized room and receive committed messages from any API replica, with bounded resource use and a clear recovery path when live delivery is missed.

**Blocked by:** 03: Publish outbox events with recoverable leases.

**Status:** ready-for-agent

- [ ] The endpoint negotiates `chat.v1`, checks session and Origin before upgrade, and implements subscribe, unsubscribe, and ping/pong frames.
- [ ] Subscription authorization is checked against current PostgreSQL membership; acknowledgement is queued before subsequent room events.
- [ ] Pub/Sub events fan out across API replicas; duplicate, missing, and out-of-order notifications do not change durable HTTP history.
- [ ] Session and membership are rechecked before private sends; access revocation or unavailable authorization stops further delivery.
- [ ] Per-socket subscriptions and outbound queues are bounded; full queues close with the specified recovery code.
- [ ] WebSocket protocol tests use real clients and verify cross-replica delivery, authorization, reconnect, and recovery via HTTP sync.
