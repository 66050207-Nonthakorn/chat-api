# 01: Apply low-risk ownership and dead-code cleanup

Type: task
Status: resolved

## Scope

Implement audit plan steps 1-3 as separately verified checkpoints, retaining public behavior and exported Go signatures except the explicitly planned internal event-channel relocation. Leave constructor, transaction, concurrency and schema compatibility changes for later increments.

- [x] Delete the obsolete message scanner, unread realtime channel field and write-only loadbench counter; replace the redundant map clone with `maps.Clone`.
- [x] Move principal/context declarations into `auth/principal.go`.
- [x] Move shared HTTP query parsing into `httpapi/query.go` and reuse it from room listing.
- [x] Move the shared event-channel name into `message` and remove realtime's production import of outbox.

## Verification

Use the existing isolated image integration Compose fixture under project `chat-maintainability-tests`. Run the full Go suite with all three test endpoints set, plus vet, command builds, formatting and diff checks after each checkpoint. Record actual outcomes below.

## Comments

- Existing `docs/codebase-map.md` edits belong to the user and must remain untouched.
- Baseline and checkpoint 1 passed `go test -p 1 -count=1 ./...` with all three isolated test endpoints configured. Checkpoint 1 also passed `go vet ./...`, `go build ./cmd/...`, gofmt and `git diff --check`. The active image-aware scanner is now named `scanMessage`; removal of the write-only counter also removed `reconcile`'s now-unused tracker parameter.
- Checkpoint 2 (principal/context file move, unchanged declarations and function bodies) passed the full configured integration suite, vet, command builds, gofmt and diff checks.
- Checkpoint 3 (shared query parsing and room-list reuse, retaining allowed keys/default 50/maximum 100 and error responses) passed the full configured integration suite, vet, command builds, gofmt and diff checks.
- Checkpoint 4 (event definitions/channel ownership in `message`, all in-repo callers migrated) passed the full configured integration suite, vet, command builds, gofmt and diff checks. `go list` confirms realtime's production imports no longer include outbox. Channel bytes and event JSON are unchanged; the internal Go helper moved from `outbox.EventChannel` to `message.EventChannel`.

## Answer

Completed all four checkpoints. Public HTTP/WebSocket behavior, SQL, transactions, admission limits and rate-limit ordering remain unchanged. No packages, interfaces or dependencies were added. Existing tests were retained; only event-channel references/imports changed in tests.

Verification passed with PostgreSQL 17, Valkey 8 and SeaweedFS 4.52:

- Baseline and every checkpoint: `go test -p 1 -count=1 ./...`, with `TEST_DATABASE_URL`, `TEST_VALKEY_URL` and `TEST_IMAGE_ENDPOINT` configured so guarded integration coverage executes.
- Every checkpoint: gofmt, `git diff --check`, `go vet ./...` and `go build ./cmd/...`.
- Final race check: `go test -race -p 1 -count=1 ./internal/auth ./internal/httpapi ./internal/realtime ./internal/outbox ./internal/loadbench`.
- The original SHA-256 of `docs/codebase-map.md` still matches exactly.
- Temporary `chat-maintainability-tests` containers, network and attached disposable volumes were removed; the project's final container list is empty.

This completes the initial low-risk increment (plan steps 1-3). The next increment is constructor/interface consolidation. The broader error-writer replacement, schema projection move and transaction consolidation remain separate changes; CI's missing image-storage fixture is still recorded in the audit.
