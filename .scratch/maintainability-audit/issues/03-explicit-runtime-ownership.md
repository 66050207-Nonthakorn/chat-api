# 03: Make HTTP logger and image admission ownership explicit

Type: task
Status: resolved

## Scope

Implement audit plan step 5 in two separately verified checkpoints.

- [x] Inject a concrete logger through HTTP options into request metadata. Resolve the default once at construction, wire the process logger explicitly in `cmd/api`, and remove process-global logger mutation from HTTP observability tests.
- [x] Inject image upload admission through HTTP options, removing the package-global gate. `cmd/api` owns one capacity-one channel shared by all HTTP handlers in that process. Handlers must not silently allocate separate capacity; callers serving uploads must supply the shared gate. Preserve immediate busy rejection, connection handling, deadlines and full-pipeline admission.

Add focused regression coverage for independent loggers, contention between handlers sharing admission, separate process-scope admission, release after failure, and rejection before reading the body. Keep SQL, image processing, S3 behavior, routing, authentication and rate-limit ordering unchanged.

## Verification

After each checkpoint: gofmt, diff checks, the full Go suite against isolated PostgreSQL/Valkey/S3-compatible storage, vet and command builds. Run a final race check for the affected HTTP runtime because admission ownership changes concurrent state wiring.

## Comments

- Preserve all prior refactoring and the user's existing `docs/codebase-map.md` edits.
- Checkpoint 1 passed the full Go suite with all three isolated test endpoints configured, vet, command builds, gofmt and diff checks. HTTP request fields, correlation and sensitive-data filtering remain covered; independent-handler logger coverage verifies that construction does not replace the process logger. The HTTP fixture now accepts constructor options directly so sync logging can inject its logger without global mutation or another fixture wrapper.
- Checkpoint 2 passed the full configured suite, vet, builds, formatting and diff checks. Its first test run exposed a test assertion on the original request's Close field, which does not observe mutations to middleware's context-bearing request copy. The assertion now checks the response's Connection: close header; application connection handling was unchanged.
- Added a deterministic public HTTP integration scenario that blocks the first upload in storage: a second handler sharing its gate returns 429, a handler with an independent gate can reach storage, and a retry after the first upload fails can acquire the released shared slot. Separate tests cover busy/missing admission before body reads. Real S3-compatible send/replay/read and deadline/streaming coverage continues to execute.

## Answer

Completed both checkpoints. `Options.Logger` is captured by metadata middleware; omitting it resolves the process default once during construction. `cmd/api` explicitly shares its logger with HTTP, realtime and runtime metrics. HTTP observability tests inject their own loggers and no longer call `slog.SetDefault`.

`Options.ImageUploadSlots` supplies the admission channel. `cmd/api` creates exactly one capacity-one channel for the API process. Upload-capable handlers in that process share it; the constructor never allocates replacement capacity. The slot remains held through image reading, normalization, storage and message persistence, with deferred release on every exit. Busy responses retain 429, Retry-After and connection-close behavior. The new explicit dependency must be supplied by upload-capable callers; if omitted, image sends return the existing 503 dependency-unavailable response before reading the body. Existing image-read paths do not require admission.

No packages, interfaces or dependencies were added. Public routes, payloads, request-log fields, authentication/rate-limit ordering, image limits, deadlines, streaming, SQL and transaction behavior were preserved for configured handlers.

Verification:

- After each checkpoint: `go test -p 1 -count=1 ./...`, with all three test endpoints configured against isolated PostgreSQL 17, Valkey 8 and SeaweedFS 4.52; `go vet ./...`; `go build ./cmd/...`; gofmt and `git diff --check`.
- Final concurrency check: `go test -race -p 1 -count=1 ./internal/httpapi` passed with the same integration services, including the new shared-admission scenario.
- Final review found no package-global upload gate or HTTP-test logger replacement. The only HTTP default logger lookup is at construction; requests use the captured logger.
- The user's `docs/codebase-map.md` SHA-256 is unchanged.
- The disposable test project was removed after verification, with an empty final project container list.
