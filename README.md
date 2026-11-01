# Cloud Chat Backend

This repository starts with the backend for the Cloud Computing chat project. The frontend is owned separately. The HTTP and WebSocket behavior follows `docs/api-contract.md`; PostgreSQL persistence follows `docs/database-schema.md` and the accepted ADRs.

## Local prerequisites

- Go 1.26 or newer
- Docker Desktop with its Linux container engine running
- Docker Compose

The local database image currently uses PostgreSQL 17 and Valkey 8. This is a local development default; confirm the production engine/version during AWS readiness work.

## Start the local dependencies

```powershell
docker compose up -d --wait postgres valkey
```

The Compose file binds both services to loopback only. The PostgreSQL named volume survives container restarts. To remove the local database and all seeded data intentionally, run `docker compose down -v`.

## Configure and start the backend

From PowerShell, set local-only values in the current shell:

```powershell
$env:APP_ENV = "local"
$env:ENABLE_LOCAL_TEST_IDENTITY = "true"
$env:DATABASE_URL = "postgres://chat:chat@localhost:5432/chat?sslmode=disable"
$env:VALKEY_URL = "redis://localhost:6379/0"
$env:HTTP_ADDR = "127.0.0.1:8080"
```

Apply schema migrations, seed the demo users/rooms, then start the API:

```powershell
go run ./cmd/migrate
go run ./cmd/seed
go run ./cmd/api
```

In another shell with the same environment, start the separate worker process:

```powershell
go run ./cmd/worker
```

The worker entry point currently monitors dependencies and shuts down on Ctrl+C; outbox processing is implemented in the follow-on backend ticket.

Check process liveness and dependency/schema readiness:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/health/live
Invoke-RestMethod http://127.0.0.1:8080/health/ready
```

Both endpoints return only a status value, include a server-generated `X-Request-ID`, and disable caching. Liveness stays healthy while dependencies are unavailable; readiness returns 503 until PostgreSQL has the supported schema and Valkey accepts a subscription.

## Local test identity

The local adapter is opt-in and only runs with `APP_ENV=local`. It recognizes the two seeded demo user IDs through the `X-Local-Test-User-ID` header. Production configuration rejects `ENABLE_LOCAL_TEST_IDENTITY=true`, and the adapter ignores this header outside local mode. Never enable the local identity adapter in AWS.

## Shutdown

Stop the API and worker with Ctrl+C. Stop local dependencies while preserving the database volume with:

```powershell
docker compose down
```

This local setup does not use AWS and does not provide production credentials.
