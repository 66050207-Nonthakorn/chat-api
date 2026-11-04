# Cloud Chat Backend

This repository starts with the backend for the Cloud Computing chat project. The frontend is owned separately. The HTTP and WebSocket behavior follows `docs/api-contract.md`; PostgreSQL persistence follows `docs/database-schema.md` and the accepted ADRs.

Image sending, retry, protected downloads and release steps are documented in [image messages](docs/image-messages.md). The [Bruno collection](bruno/cloud-chat-local/README.md) includes text and image requests with a bundled PNG fixture.

## Local prerequisites

- Go 1.26 or newer
- Docker Engine with Linux containers (Docker Desktop on Windows, or Docker Engine on Linux)
- Docker Compose plugin

The local database image currently uses PostgreSQL 17 and Valkey 8. This is a local development default; confirm the production engine/version during AWS readiness work.

## Start the local dependencies

```powershell
docker compose up -d --wait postgres valkey media
```

The Compose file binds PostgreSQL, Valkey, and the shared S3-compatible image store to loopback only. The local image bucket is created by SeaweedFS from the development environment settings. PostgreSQL and image data use named volumes that survive container restarts. To remove all local database and image data intentionally, run `docker compose down -v`.

## Configure and start the backend

On Windows, set local-only values in PowerShell:

```powershell
$env:APP_ENV = "local"
$env:ENABLE_LOCAL_TEST_IDENTITY = "true"
$env:DATABASE_URL = "postgres://chat:chat@localhost:5432/chat?sslmode=disable"
$env:VALKEY_URL = "redis://localhost:6379/0"
$env:HTTP_ADDR = "127.0.0.1:8080"
```

On Linux, set the same values in Bash:

```bash
export APP_ENV=local
export ENABLE_LOCAL_TEST_IDENTITY=true
export DATABASE_URL='postgres://chat:chat@localhost:5432/chat?sslmode=disable'
export VALKEY_URL='redis://localhost:6379/0'
export HTTP_ADDR='127.0.0.1:8080'
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

The API, authenticated WebSocket server, and outbox worker are separate processes. The worker claims committed outbox events, publishes through Valkey, records bounded retries, and cleans up uncommitted image uploads. In local mode image sending is enabled by default; production keeps it disabled until all API and worker tasks run the image-compatible release. Readiness fails when dependencies are unhealthy or an API process is draining.

Check process liveness and dependency/schema readiness:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/health/live
Invoke-RestMethod http://127.0.0.1:8080/health/ready
```

On Linux, use `curl`:

```bash
curl --fail http://127.0.0.1:8080/health/live
curl --fail http://127.0.0.1:8080/health/ready
```

Both endpoints return only a status value, include a server-generated `X-Request-ID`, and disable caching. Liveness stays healthy while dependencies are unavailable; readiness returns 503 until PostgreSQL has the supported schema and Valkey accepts a subscription.

## Integration checks

Image rollout and streaming regressions use an isolated local stack:

```powershell
docker compose -p chat-image-hardening-tests -f .scratch/image-messages/compose.test.yaml up -d --wait
$env:TEST_DATABASE_URL = "postgres://chat:chat@localhost:25433/chat?sslmode=disable"
$env:TEST_VALKEY_URL = "redis://localhost:26380/0"
$env:TEST_IMAGE_ENDPOINT = "http://localhost:28334"
go test -p 1 ./...
docker compose -p chat-image-hardening-tests -f .scratch/image-messages/compose.test.yaml down -v
```

The test creates isolated database schemas and random object keys. The explicit Compose project above owns only disposable test services. The suite checks schema-1 text traffic through migration 0002, image replay/outbox/member authorization, stalled uploads, overload rejection, and streaming/truncated downloads. See [shared edge integration](infra/shared-edge/README.md) for the offline CloudFront config check; it does not contact AWS.

Set the test URLs to the local Compose services before running the PostgreSQL/Valkey integration tests:

```powershell
$env:TEST_DATABASE_URL = "postgres://chat:chat@localhost:5432/chat?sslmode=disable"
$env:TEST_VALKEY_URL = "redis://localhost:6379/0"
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./cmd/...
```

On Linux, set the test URLs and run the same checks:

```bash
export TEST_DATABASE_URL='postgres://chat:chat@localhost:5432/chat?sslmode=disable'
export TEST_VALKEY_URL='redis://localhost:6379/0'
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./cmd/...
```

The Windows and Linux scripts under `infra/scripts/` build both container images from a clean commit and verify their source revision label and content ID. GitHub Actions runs the same backend checks against service containers; it builds local images but does not push, deploy, or apply AWS infrastructure.

## Local test identity

The local adapter is opt-in and only runs with `APP_ENV=local`. It recognizes the two seeded demo user IDs through the `X-Local-Test-User-ID` header. Production configuration rejects `ENABLE_LOCAL_TEST_IDENTITY=true`, and the adapter ignores this header outside local mode. Never enable the local identity adapter in AWS.

## Shutdown

Stop the API and worker with Ctrl+C. The API marks readiness unavailable, drains HTTP requests, closes WebSockets with a going-away status, and force-closes at the configured grace deadline. The worker stops claiming, finishes or cancels active publishes, and releases any remaining leases so a replacement can continue promptly. Stop local dependencies while preserving the database volume with:

```powershell
docker compose down
```

This local setup does not use AWS and does not provide production credentials.

For immutable ECR publication, the controlled one-off migration task, compatible schema rollout/rollback, Terraform telemetry, and unverified live-environment checks, see [the infrastructure operations guide](infra/README.md).
