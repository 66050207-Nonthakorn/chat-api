# 02: Consolidate HTTP construction and image storage interfaces

Type: task
Status: resolved

## Scope

Implement audit plan step 4 in two separately verified changes. Preserve HTTP/WebSocket routes, response envelopes, middleware order, nil-dependency behavior, production fail-closed rate limiting, and image streaming/cleanup behavior.

- [x] Replace the four HTTP constructors with `NewHandler(cfg, pool, Options)` and migrate every in-repo caller. Use one plain struct for readiness, realtime, authentication, rate limiter and image storage; remove the variadic image-store argument and delegating constructors.
- [x] Move the Put/Get image-store interface to the HTTP consumer and the Delete-only interface to cleanup. Remove the producer-owned `imagestore.Store` interface and the test double's unrelated Delete method.

Configuration and PostgreSQL remain explicit constructor arguments. Resource admission, logger injection, SQL/transaction changes and other packages' constructors are outside this increment.

## Verification

After each change: gofmt, `git diff --check`, the full Go suite against isolated PostgreSQL/Valkey/S3-compatible storage, `go vet ./...` and `go build ./cmd/...`. Retain existing public HTTP/WebSocket and real image-storage integration coverage. Record actual results below.

## Comments

- The prior increment's code and the user's pre-existing `docs/codebase-map.md` edits must remain intact.
- Checkpoint 1 passed gofmt, `git diff --check`, `go test -p 1 -count=1 ./...` with all three isolated test endpoints set, `go vet ./...` and `go build ./cmd/...`. Existing auth, rate-limit, WebSocket and real image-storage coverage executed. Added endpoint-level regression coverage for optional dependencies, health availability, response metadata/envelopes and production fail-closed behavior without a limiter. The canonical constructor changes internal Go call signatures; public HTTP/WebSocket contracts remain the same.
- Checkpoint 2 passed the same full configured suite, vet, builds, formatting and diff checks. HTTP owns a private Put/Get `imageStore` interface; cleanup owns a private Delete-only `imageDeleter` interface and no longer imports `imagestore`. Existing concrete S3 callers satisfy both implicitly, and the streaming test double no longer supplies an unused Delete method.

## Answer

Completed audit plan step 4 in two verified checkpoints. `httpapi.NewHandler(cfg, pool, Options)` is the sole HTTP constructor. All in-repo callers now name their optional readiness/WebSocket/auth/rate-limiter/image dependencies. Removed the three delegating constructors and variadic image-store argument. The shared `imagestore.Store` interface was replaced by the two consumer-owned contracts; the concrete S3 implementation and object-read result are unchanged. No packages or dependencies were added.

Preserved metadata -> local identity -> session auth -> rate limiting -> routing order, endpoint-specific missing-dependency responses, health availability, local optional limiter behavior and production fail-closed rate limiting. Existing upload admission, streaming I/O, cleanup SQL, transaction behavior and process lifecycle ownership remain unchanged.

Verification after each checkpoint:

- `go test -p 1 -count=1 ./...`, with `TEST_DATABASE_URL`, `TEST_VALKEY_URL` and `TEST_IMAGE_ENDPOINT` configured against isolated PostgreSQL 17, Valkey 8 and SeaweedFS 4.52.
- `go vet ./...` and `go build ./cmd/...`.
- gofmt and `git diff --check`.

Final review confirmed no remaining Go references to the removed constructors or `imagestore.Store`; cleanup's import list no longer contains the S3 storage package. The user's `docs/codebase-map.md` SHA-256 still matches its original value exactly. Temporary `chat-maintainability-tests` services, network and disposable volumes were removed, with an empty final project container list.

The next planned increment is explicit logger/upload-admission ownership. It is separate from the constructor and interface changes completed here.
