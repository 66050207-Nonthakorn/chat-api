# Chat Backend Task Map

## Notes

- Scope is backend only; the frontend is separately owned.
- Ticket verification is local unless its answer explicitly records a live external check.
- `docs/adr/references/08-backend-load-and-recovery-evidence.md` contains measured local load and recovery results.

## Decisions-so-far

- [01: Local backend foundation](issues/01-local-backend-foundation.md) — resolved; PostgreSQL/Valkey Compose, migrations, seed, and health endpoints.
- [02: Persist and recover messages](issues/02-persist-and-recover-messages.md) — resolved; authorized HTTP persistence, idempotency, history, and sync.
- [03: Recoverable outbox worker](issues/03-recoverable-outbox-worker.md) — resolved; fenced outbox leases and Valkey publication.
- [04: Authorized WebSocket delivery](issues/04-authorized-websocket-delivery.md) — resolved; authorized cross-replica delivery and bounded sockets.
- [05: Cognito-backed sessions](issues/05-cognito-sessions.md) — resolved; fake-OIDC-tested backend sessions and revocation.
- [06: Backend AWS infrastructure](issues/06-backend-aws-infrastructure.md) — resolved; non-applying Terraform plan only.
- [07: Backend release and operations](issues/07-backend-release-and-operations.md) — resolved; local CI/build, controlled migration, bounded drain, and telemetry.
- [08: Backend load and recovery evidence](issues/08-backend-load-and-recovery-evidence.md) — resolved; local benchmark and failure drills with AWS capacity checks unverified.
- [09: Shared API and WebSocket rate limits](issues/09-shared-rate-limiting.md) — resolved; cross-replica HTTP and WebSocket token buckets use Valkey and fail closed.
- [10: Shared persisted-session rules](issues/10-shared-session-rules.md) — claimed; centralize session eligibility and atomic idle renewal across HTTP and WebSocket.

## Fog

- Live Cognito verification, AWS apply, image publication, live CI, CloudWatch alarm evaluation, RDS failover, ECS autoscaling, ALB behavior, and AWS cleanup require external account/network setup and remain unverified.
- OpenTelemetry trace export is deferred; current correlation is through request IDs in structured logs.
