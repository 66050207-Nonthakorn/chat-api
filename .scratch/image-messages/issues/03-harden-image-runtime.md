# 03 — Harden image rollout and I/O

Type: task
Status: resolved
Blocked by: 02

## Goal

Support a schema-1 compatibility rollout before migration 0002, bound upload admission and body reads, stream authorized image downloads, and prepare the shared CloudFront integration locally. Add regression tests and run checks. Do not deploy or change AWS resources.

## Answer

- API readiness, text send/replay/history/sync, outbox and cleanup support schemas 1 and 2. Schema 1 is allowed only with image sends disabled; unknown schema versions fail closed. Roll out these compatible binaries to all API/worker tasks before applying migration 0002. The earlier schema-1-only binaries remain unsuitable rollback targets after migration.
- One image pipeline per API task, with immediate 429/Retry-After when full. Multipart input has a real 45-second socket read deadline. Failed or rejected reads close the connection instead of leaving an unbounded body drain; successful reads restore keep-alive.
- Authorized image GET checks S3 object length before sending headers and streams with bounded I/O. Truncated/read/write failures abort the transfer instead of appending an error body. Preserve private no-store/nosniff headers.
- Shared CloudFront origin/behavior fragment and `cmd/check-edge` provide an offline check for disabled caching, cookie/header/query forwarding, private route precedence, methods, HTTPS/TLS and timeouts. The externally owned distribution is not changed or proven live; merge/check its configuration before enabling images.

## Verification

- `go test -p 1 ./...` passed against isolated PostgreSQL 17, Valkey 8 and SeaweedFS 4.52 containers.
- `go test -race -p 1 ./internal/httpapi ./internal/outbox ./internal/realtime ./internal/migrations ./cmd/check-edge` passed with those local services.
- Integration covered schema-1 text send/replay/history/sync/outbox and cleanup; applying migration 0002 twice; readiness through the change; image send/replay/conflict, real S3 Put/Get metadata, alpha, image outbox payload, reads with sending disabled, and member removal.
- Real HTTP regressions covered stalled-body deadlines, immediate overload rejection, receiving the first image chunk before the rest of the object is read, size mismatch and truncated transfers.
- `go vet ./...`, `go build ./...`, edge checker fixture validation, and `git diff --check` passed.
- `infra/scripts/windows/plan-offline.ps1` passed Terraform formatting, validation and an isolated plan with dummy credentials. No apply, publish, deployment, or live AWS verification occurred.

The disposable Compose project is `chat-image-hardening-tests` from `../compose.test.yaml`; its services are removed after testing. Existing Docker services are not part of that project.
