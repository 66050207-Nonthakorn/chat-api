# Cloud Chat Backend

Go backend for Cloud Chat. The API serves HTTP and authenticated WebSocket connections. PostgreSQL stores messages and pending events. A separate worker publishes those events through Valkey for real-time delivery. Images are stored in an S3-compatible bucket.

## Local setup

Install Go 1.26+ and Docker with the Compose plugin. Run the commands below from the repository root.

### 1. Start dependencies

```sh
docker compose up -d --wait postgres valkey media
```

Compose starts PostgreSQL, Valkey, and SeaweedFS for local image storage. The services bind to loopback.

### 2. Load the development settings

Load `.env.example` in each shell where you run a Go command. It points to the local Compose services and enables the seeded demo identities.

**Bash or Zsh**

```sh
set -a
. ./.env.example
set +a
```

**PowerShell**

```powershell
Get-Content .env.example | Where-Object { $_ -match '^\w+=.+' } | ForEach-Object {
    $name, $value = $_ -split '=', 2
    Set-Item "Env:$name" $value
}
```

### 3. Migrate, seed, and start

Apply the schema, seed the demo users and rooms, then start the API:

```sh
go run ./cmd/migrate
go run ./cmd/seed
go run ./cmd/api
```

Open a second terminal, load the development settings again, and start the worker:

```sh
go run ./cmd/worker
```

### 4. Check the API

```sh
curl -f http://127.0.0.1:8080/health/live
curl -f http://127.0.0.1:8080/health/ready
```

Liveness checks that the API is responding. Readiness checks that PostgreSQL has the supported schema and Valkey accepts subscriptions. Open the [Bruno collection](bruno/cloud-chat-local/README.md) to try text messages, WebSockets, and image uploads with the seeded users. The demo uses the local-only `X-Local-Test-User-ID` header.

Stop the API and worker with Ctrl+C, then run `docker compose down`. PostgreSQL and image data persist in named volumes.

## Documentation

- [API contract](docs/api-contract.md) and [database schema](docs/database-schema.md)
- [Image messages and rollout](docs/image-messages.md)
- [AWS deployment and operations](infra/README.md)
