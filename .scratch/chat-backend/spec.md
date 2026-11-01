# Chat Backend Implementation

**Status:** Approved backend-only scope.

Implement the server-side portion of the Cloud Computing chat design. The existing documents remain the technical source of truth:

- `docs/api-contract.md` — HTTP, WebSocket, authentication, error, and health contracts
- `docs/database-schema.md` and `docs/database-schema.sql` — PostgreSQL schema and transaction rules
- `docs/adr/0001-durable-messaging-and-realtime.md` — durable messages, outbox, and cross-replica delivery
- `docs/adr/0002-identity-sessions-and-membership.md` — identity, sessions, CSRF, and membership authorization
- `docs/adr/0003-aws-hosting-and-delivery.md` — AWS hosting decisions
- `docs/adr/references/02-implementation-and-acceptance.md` — M1–M6 milestones and acceptance evidence

## Scope

- Go API, migration and seed tooling, PostgreSQL persistence, local-only test identity, outbox worker, Valkey Pub/Sub, WebSocket server, and Cognito-backed sessions.
- Protected Terraform state plus backend-owned AWS resources and backend release, health, recovery, and measurement tooling.
- Use public HTTP, worker/PubSub, WebSocket, auth, and Terraform plan seams approved in this chat for TDD.

## Out of scope

- Svelte UI, IndexedDB, frontend build/release, S3/CloudFront, and frontend routing.
- Read receipts, typing/presence, message edit/delete, attachments, public signup, and other deferred product features.
- AWS apply, live Cognito verification, and cloud performance/failover claims until the account, region, credits, and shared network inputs are available.

The numbered tickets in `issues/` implement this scope in dependency order. Do not describe planned or locally mocked behavior as live AWS verification.
