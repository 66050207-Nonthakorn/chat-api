# 01: Run a local backend foundation

**Type:** task

**What to build:** A developer can start the backend locally, apply its schema and repeatable demo seed, and check process and dependency health without AWS.

**Blocked by:** None (can start immediately).

**Status:** resolved

- [x] The Go API, worker, and migration commands build as separate entry points.
- [x] Docker Compose provides PostgreSQL and Valkey for local development.
- [x] Migrations apply from an empty database; demo users and rooms can be seeded repeatedly without duplicates.
- [x] `/health/live` reports process liveness, and `/health/ready` reports dependency/schema readiness without exposing internals.
- [x] Local test identity is explicitly disabled outside local mode.
- [x] Startup, migration, seed, and shutdown commands are documented.

## Answer

Implemented in `31937de` and merged into `integration` at `bf425b7`.

Verification: `go test ./...`, `go vet ./...`, and `go build ./cmd/...` passed; `docker compose config` passed; against PostgreSQL 17 and Valkey 8, migration and seed each ran twice successfully, and both health endpoints returned 200 with status-only JSON, distinct request IDs, and `Cache-Control: no-store`.
