# 09: Enforce shared API and WebSocket rate limits

**Type:** task

**What to build:** Enforce the existing API contract limits atomically across backend replicas using the existing Valkey service. Fail closed when shared limiter state is unavailable.

**Blocked by:** 01: Run a local backend foundation; 04: Stream authorized messages over WebSocket; 05: Add Cognito-backed sessions; 06: Define backend AWS runtime infrastructure.

**Status:** resolved

- [x] Message-send HTTP requests are limited to 5 per second per user with burst 10, including idempotent replay.
- [x] Authenticated read API requests are limited to 20 per second per user with burst 40 across replicas.
- [x] WebSocket handshakes are limited to 10 per minute per user with burst 5 and return HTTP 429 plus `Retry-After` before upgrade.
- [x] WebSocket commands are limited to 5 frames per second with burst 10 per connection; excess returns `RATE_LIMITED` and repeated excess closes with 1008.
- [x] Shared atomic limiter state is isolated by environment and operation; there is no per-replica fallback.
- [x] Limiter failure returns HTTP 503 with `Retry-After` for limited HTTP routes and fails closed for upgraded sockets.
- [x] Public HTTP and WebSocket tests against Valkey verify shared behavior across two API instances, boundary values, retry timing, and dependency failure.

The source of the exact limits and failure behavior is `docs/api-contract.md`, section 7.

## Answer

Implemented in `24d4b84` and merged into `integration` at `24d4b84`.

Verification: public HTTP/WebSocket integration tests against shared PostgreSQL and Valkey cover two API instances, authenticated identity scoping, send replay charging, read bursts/refill, handshake 429 before upgrade, per-connection command bursts, repeated-excess 1008, and fail-closed 503/1013 behavior. `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, and `go build ./cmd/...` passed.

The limiter uses an atomic Valkey token bucket keyed by environment, operation, and hashed identity. Production WebSocket startup rejects missing limiter configuration. Local unit-test constructors may omit the limiter only in local mode.
