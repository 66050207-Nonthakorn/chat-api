# Go maintainability audit and refactoring plan

Date: 2026-11-05

Scope: the current working tree's Go commands, internal packages, tests, local tracker, related ADRs, and backend CI. This is an architectural audit, not an implementation change. Source locations below refer to the audited tree.

## Assessment

Keep the modular monolith and its existing package set. The import graph is acyclic, the commands explicitly construct their dependencies, and PostgreSQL transactions are visible rather than hidden behind generic repository layers. Most complexity in sessions, outbox leases, image normalization, and WebSocket authorization implements documented requirements.

The main problem is that message-write invariants have two owners inside `httpapi`: text sending and image sending. Other actionable problems are incremental constructor wrappers, a runtime message query in `migrations`, a subscriber importing its publisher's worker package, process-global mutable state, and focused tests located outside their owning packages. None requires a new package or a general service/repository framework.

The existing uncommitted `docs/codebase-map.md` changes were left untouched.

## Current ownership and dependencies

| Responsibility | Current owner | Assessment |
| --- | --- | --- |
| Process construction, signals, readiness dependencies, shutdown | `cmd/api`, `cmd/worker` | Appropriate composition roots; keep concrete construction here. |
| HTTP routing, request decoding, room queries, message writes and image delivery | `internal/httpapi` | Appropriate HTTP owner, but duplicated message transactions and shared HTTP helpers buried in feature files. |
| OIDC, encrypted session tokens, refresh coordination and persisted-session eligibility | `internal/auth` | Cohesive responsibility; preserve `SessionRules`, transaction locks and refresh fencing. Core principal declarations are misleadingly located in the local identity file. |
| Shared message/event representation | `internal/message` | Appropriate common owner for message protocol facts. Currently only payload types/constants. |
| Committed notification claims, retry and publishing | `internal/outbox` | Appropriate durable worker owner. Its channel-name function is shared protocol knowledge rather than worker behavior. |
| WebSocket subscriptions, delivery, authorization and backpressure | `internal/realtime` | Cohesive package; one file contains several distinct state owners. Unnecessary dependency on `outbox`. |
| Image normalization, S3 access and orphan cleanup | `imageproc`, `imagestore`, `imagecleanup` | Distinct, justified ownership: CPU transformation, object I/O and database-led cleanup. Keep them separate. |
| Schema application/version compatibility | `internal/migrations` | Appropriate except for message-specific SELECT join construction. |
| Shared infrastructure and transport primitives | `db`, `ratelimit`, `health`, `apierror`, `httpjson`, `strictjson`, `requestid`, `uuid` | Mostly small and useful. Small size alone is not a reason to merge them. |
| Demo provisioning, workload measurement and fixtures | `demo`, `seed`, `loadbench`, `testutil` | Appropriate separation from runtime business operations; workload config is misplaced in the statistics file. |

Significant current production import paths:

```text
cmd/api -> httpapi, auth, realtime, ratelimit, imagestore, migrations, health, db, config
cmd/worker -> outbox, imagecleanup, imagestore, db, config
httpapi -> auth, imageproc, imagestore, message, migrations, ratelimit
outbox -> message, migrations
realtime -> auth, message, outbox, ratelimit
imagecleanup -> imagestore, migrations
```

HTTP execution order is currently `requestMetadata -> LocalTestIdentityMiddleware -> auth.Middleware -> rateLimitMiddleware -> route handler`. The constructor delegation chain obscures this order. Preserve it during refactoring, including authentication before charging rate limits and fail-closed production behavior.

## Ranked findings

Severity describes maintenance impact, not a claim that an exploitable bug exists. Risk describes the chance of changing behavior while refactoring.

| ID | Severity | Refactoring risk | Problem |
| --- | --- | --- | --- |
| A1 | High | High | Text and image writes duplicate the transaction protocol. |
| A2 | Medium | Medium | HTTP construction has four entry points and a variadic single dependency. |
| A3 | Medium | Medium | Image admission and HTTP logging depend on mutable process globals. |
| A4 | Medium | Medium | `migrations` owns message SELECT shape and implicit SQL aliases. |
| A5 | Medium | Low | Realtime imports outbox solely for the event channel name. |
| A6 | Medium | Low-Medium | Focused session/image tests lack locality; CI silently skips image storage rollout coverage. |
| A7 | Medium | Medium | The seed command loads configuration for unrelated runtime responsibilities. |
| A8 | Low | Low | S3 defines a producer-owned interface broader than cleanup needs. |
| A9 | Low | Low | Proven dead code, an unused-argument wrapper, and repeated request parsing obscure the active implementation. |
| A10 | Low | Low-Medium | Several files mix distinct responsibilities despite sensible package ownership. |

### A1: One message transaction protocol, two implementations

Evidence: `internal/httpapi/messages.go:43` (`sendMessage`) and `internal/httpapi/images.go:239` (`persistImageMessage`). The image operation also starts at `images.go:45` (`sendImageMessage`).

Both transaction implementations revalidate sessions, lock the active sender, lock the room, check membership, look up an idempotency key, allocate a room sequence, insert the message and insert its outbox row before committing. They separately assemble the success payload. The sequence implementations already differ: text uses a guarded increment with RETURNING; images read/increment/write explicitly. That difference is not evidence of incorrect current behavior, but it increases the work needed to verify every future change.

`persistImageMessage` is also a database operation that accepts both an HTTP response writer/request and a separate context, and writes transport responses itself. That couples persistence decisions to HTTP error handling and makes the real transaction operation hard to exercise independently.

Target: first consolidate the shared lock/precondition and insertion logic as private functions in `httpapi/message_write.go`. Work toward one private message-write operation returning the stored message and replay flag, with a small concrete input containing optional image candidate metadata. Use operation-specific error outcomes for existing HTTP mappings. Keep request parsing, multipart I/O, S3 writes and response encoding outside that operation. Do not add a `MessageService`, `Repository` interface, callback-driven transaction framework or new package.

Preserve image preflight membership checks before reading the body, normalization and object upload before final attachment, source-byte fingerprint comparisons, text/image idempotency conflicts, account/session/room rejection order, row lock order, and atomic sequence/message/outbox commit. Do not put S3 I/O inside the database transaction. Consolidate in small changes rather than replacing both paths in one patch.

### A2: Constructor history has become the interface

Evidence: `internal/httpapi/handler.go:37`, `:43`, `:49`, `:56`: `NewHandler -> NewHandlerWithRealtime -> NewHandlerWithAuth -> NewHandlerWithAuthAndRateLimiter`.

The wrappers fill dependencies with nil until reaching the real constructor. Its variadic `imageStores ...imagestore.Store` accepts arbitrary cardinality but uses only element zero. This represents compatibility with earlier construction forms rather than an actual collection of stores. Callers must infer which constructor has the required production behavior.

Target: one canonical constructor with a plain options/dependencies struct for optional realtime, authentication, rate limiter, image storage, readiness and logger values. Keep required configuration and PostgreSQL explicit. Avoid functional-option functions, builders or factories. Migrate known callers together, then remove obsolete constructors; all are under `internal`, but their exported Go signatures still constitute a source interface change and must be called out in the implementation summary. HTTP/WebSocket interfaces and nil-dependency behavior stay unchanged.

The two outbox constructors are a much smaller convenience seam with real test callers; do not expand this cleanup into a mandatory constructor rewrite for every package.

### A3: Resource scope and logging dependencies are implicit

Evidence: `internal/httpapi/images.go:37` (`imageUploadSlots`), `images_test.go:31` (test directly occupies that global), `internal/httpapi/handler.go:141` (`requestMetadata`) and `internal/httpapi/observability_test.go:17`/`:52` (`slog.SetDefault`).

All handler instances share image admission state. A test or embedded handler can affect another without any dependency being passed. Request logging resolves the process logger for each request, and tests replace it globally. The package is consequently harder to run independently or test concurrently.

Target: pass the upload admission channel explicitly and store it on the handler; pass a concrete `*slog.Logger` to metadata middleware. Construct one capacity-one gate in `cmd/api`, and explicitly share it between handlers that represent one API process. Give unrelated test instances their own gate. This is dependency wiring, not a new limiter abstraction.

Important behavioral constraint: blindly allocating a new gate in every constructor changes the existing process-wide memory bound. Preserve the documented one-image-pipeline-per-API-task policy. Preserve immediate overload rejection, Retry-After, connection-close decisions and body deadlines. Logger defaults may remain a construction convenience, resolved once rather than looked up for every request.

### A4: Runtime message SQL does not belong to migration execution

Evidence: `internal/migrations/compatibility.go:32` (`MessageImageJoin`), called by text send/replay, history/sync and `internal/outbox/worker.go:295` (`claimBatch`).

This function queries the schema version and then returns a message projection fragment that assumes aliases `m` and `images` and a fixed column shape. Its placement makes migration code an owner of runtime message reads. The returned SQL's caller obligations are implicit in a comment and string concatenations.

Target: retain `Version`, `CheckVersion`, `CurrentVersion` and migration execution in `migrations`. Move only the pure, version-specific message projection selection to `internal/message/projection.go`; callers obtain the version at the same point as today and pass it explicitly. Document its fixed aliases/columns at that function. The existing shared message package then owns shared message read representation as well as payloads, without acquiring database connections or a repository layer. This is a deliberate small expansion of that package's ownership.

Keep rechecking versions per operation so already-running binaries can observe migration 0002. Keep schema-1 null image columns, schema-2 images and rejection of unknown versions. Do not remove compatibility branches until supported rollout/rollback policy changes explicitly.

### A5: A subscriber depends on the durable publishing worker

Evidence: `internal/realtime/server.go:94` calls `outbox.EventChannel`; `internal/outbox/worker.go:92` defines it. This is realtime's only production use of `outbox`.

Target: move `EventChannel(environment string)` to `internal/message/event.go`, alongside the existing `CreatedType` and event representation. Update both publisher and subscriber plus tests. This removes the production `realtime -> outbox` edge without a new broker package or interface. Keep the exact channel string `cloudchat:<environment>:events`.

### A6: Tests are organized around the HTTP fixture rather than responsibility

Evidence: `internal/httpapi/session_rules_integration_test.go:15` tests `auth.SessionRules` through `newAuthIntegrationApp`, performing OIDC login to construct a persisted-session fixture. `internal/auth/session_rules_test.go` only covers validation before querying. `internal/imageproc` has no direct tests. Image normalization, storage, cleanup and rollout are bundled into `internal/httpapi/image_rollout_integration_test.go:31`.

The workflow `.github/workflows/backend-ci.yml` supplies PostgreSQL and Valkey but neither image storage nor `TEST_IMAGE_ENDPOINT`. The image rollout test skips when any of its three environment variables is absent. A successful CI run therefore does not establish image rollout/storage coverage.

Target: move focused persisted-session eligibility/renewal tests into `auth`, using a direct database fixture through existing `testutil`. Keep HTTP login/logout/CSRF tests in `httpapi`. Add meaningful direct normalization cases in `imageproc` for supported formats, invalid/oversized images, orientation and transparency; retain the end-to-end image HTTP test. Put focused cleanup state-machine tests in `imagecleanup` when changing that logic. Add an explicit integration job using the existing isolated Compose fixture and require all three test environment variables before running it.

This is a prerequisite for risky message/image refactoring, not a request to replace public behavior tests with mocks or create another test harness package. Keep schema isolation and cleanup safeguards in `testutil`.

### A7: Database provisioning depends on API secrets

Evidence: `cmd/seed/main.go:22` calls `config.Load()`, although its actual operation only needs a database URL. `internal/config/config.go:58`/`:168` load and validate cursor keys, image storage and OIDC/session settings for that path.

Adding an API startup requirement can thus affect the seed command. `cmd/migrate` already reads its database configuration independently, and `LoadWorker` deliberately excludes API authentication configuration.

Target: give the seed command a database-only configuration path, preserving database validation and local defaults. Do not automatically split every runtime configuration into nested objects or create a new package. Reducing unrelated required configuration changes command startup behavior, so implement this separately with explicit justification and configuration tests rather than presenting it as a behavior-neutral move.

### A8: Keep the storage seam, reduce its scope

Evidence: `internal/imagestore/s3.go:17` defines `Store` with Put/Get/Delete; `internal/imagecleanup/worker.go:16`/`:31` accept it but only call Delete. The HTTP path uses Put/Get. The test store in `internal/httpapi/images_test.go:23` supplies irrelevant no-op methods to satisfy the shared interface.

Target: define the needed Put/Get interface at the HTTP consumer, and a Delete-only interface at cleanup's consumer. Keep `imagestore.S3` concrete and `imagestore.Object` as the object-read result. Do not replace the seam with `*imagestore.S3` in HTTP: the gated/truncated stream tests are real consumers of substitution. Do not introduce interfaces for every SDK dependency merely for symmetry.

Other existing tiny interfaces are justified: migration `queryer` accepts both transactions and pools; the scanner accepts both rows and a single row. `checkRoomMembership` currently only receives a pool, so its anonymous query interface can become a concrete pool parameter if it remains separate. Reassess after A1 rather than exporting another database interface.

### A9: Mechanical deletion and misleading helper names

- `delete:` `internal/httpapi/messages.go:251`, old `scanMessage`: no callers; all live reads use the image-aware scanner. Delete it, then rename `scanMemberMessage` to `scanMessage`; scanning itself does not check membership.
- `delete:` `internal/realtime/server.go:61`/`:108`, `Server.channel`: assigned but never read. Keep the local channel variable used when subscribing.
- `delete:` `internal/loadbench/runner.go:164`/`:661`/`:675`, `tracker.syncErrors`: written but never read. The local `syncErrors` returned by `reconcile` already populates reports; retain that value.
- `shrink:` `internal/httpapi/handler.go:135`/`:137`, `errorDetail` alias and `writeError`: the wrapper discards its request argument and delegates unchanged to `apierror.Write`. Use the actual error writer/type, or retain a convenience only if it really adds behavior. Do not change JSON envelopes.
- `shrink:` `internal/httpapi/rooms.go:19`, `listRooms`: repeats query-key and page-size validation already implemented by `validateQueryKeys`/`parseLimit` in `messages.go:524`/`:538`. Reuse those functions with the same allowed keys, defaults and limits; relocate the shared HTTP parsing helpers to `httpapi/query.go`.
- `stdlib:` `internal/loadbench/runner.go:1040`, `cloneMap`: `maps.Clone` provides the existing operation for the non-nil tracker maps used here. Check nil semantics before substituting it for any additional caller.

Conservative mechanical-cleanup estimate, excluding the larger refactors: net about -30 lines, -0 dependencies possible. Do not use a line-reduction target to justify broader edits. The unused-argument wrapper cleanup has many call sites and can wait if its churn would obscure a transaction change.

### A10: File moves with concrete ownership reasons

These are lower priority than A1-A6 and do not justify new packages:

- Move `Principal`, `principalKey`, `ContextWithPrincipal`, `PrincipalFromContext` from `internal/auth/local_test_identity.go:13`/`:21`/`:40`/`:44` to `auth/principal.go`. Production identity currently appears to be owned by a local-test adapter. Keep the adapter/header in the original file.
- Move OIDC discovery/client setup and its metadata type from `auth/server.go:69`/`:146`/`:153` to `auth/oidc.go`, and cookie policy/functions from `server.go:615` onward to `auth/cookie.go` when editing those responsibilities. Leave token refresh in `refresh.go`, encryption in `encryption.go` and shared eligibility in `session_rules.go`. Do not create a generic identity-provider interface.
- Organize realtime's existing state owners within the same package: server lifecycle/broker dispatch in `server.go`, `socket` and its loops/subscription operations in `socket.go`, `messageQueue` and its byte/frame accounting in `queue.go`, command/frame declarations and validation in `protocol.go`. The reason is making concurrency ownership and lock discipline locally readable; preserve fields, method bodies and lock ordering during mechanical moves. The queue cannot be replaced by a plain channel without losing byte limits and selective event removal.
- Move `loadbench.Config`/`Validate` from `stats.go:54`/`:71` to `loadbench/config.go`. Keep statistics independent of workload validation. When revisiting the runner, group Docker observation (`resourceSampler`) in `sampler.go` and report construction/types in `report.go`, both in `loadbench`. Keep `Run` as the coordinator; avoid a plugin architecture for a single observer.

## Target organization

No new packages. The files below show ownership, not a requirement to perform every move immediately.

```text
cmd/api/main.go                 concrete wiring, shared image gate, process lifecycle
cmd/worker/main.go              outbox/cleanup construction and lifetime
internal/httpapi/
  handler.go                   canonical constructor and existing route dispatch
  middleware.go                request metadata and observed response writer
  query.go                     shared HTTP query validation
  rooms.go, cursor.go           room HTTP operations and room cursor signing
  messages.go                  text request handling, history and sync
  message_write.go             private common message transaction operation
  message_queries.go           HTTP-owned projections/scanning
  images.go                    multipart/S3 orchestration and authorized streaming
  rate_limit.go                HTTP rate-limit mapping
internal/auth/
  principal.go                 production identity and context access
  local_test_identity.go       local demo identity adapter
  server.go, oidc.go, cookie.go HTTP auth workflows and OIDC/cookie responsibilities
  session_rules.go             shared persisted-session eligibility/idle renewal
  session_lock.go, refresh.go  write locks and refresh lease coordination
  encryption.go                token encryption
internal/message/
  message.go, event.go          shared payloads, event type and event channel
  projection.go                pure version-specific shared message projection
internal/realtime/
  server.go, socket.go          process/connection state owners
  queue.go, protocol.go         backpressure state and wire protocol
internal/migrations/            migration execution and version compatibility
internal/outbox/                durable notification claims, leases and publication
internal/imageproc/             pure image transformation
internal/imagestore/            concrete S3 object I/O
internal/imagecleanup/          candidate deletion/cleanup state machine
internal/loadbench/             workload config, runner, reporting, sampling, stats
```

Target imports remove `realtime -> outbox`. Both use `message.EventChannel`. Runtime message callers still depend explicitly on `migrations.Version`; shared message projection receives a version and performs no I/O. `auth`, `outbox` and `imagecleanup` keep their respective session, lease and cleanup invariants.

## Incremental implementation plan

Each numbered step is a separate reviewable change. Do not combine mechanical file moves with transaction or concurrency changes.

| Step | Change | Required verification |
| --- | --- | --- |
| 0 | Establish integration execution, including local S3; make CI's missing image fixture explicit. Record baseline failures/skips. | Existing full suite against isolated PostgreSQL/Valkey/S3; `go vet ./...`; `go build ./cmd/...`. Verify the image rollout test actually executes. |
| 1 | Delete the obsolete scanner, unread realtime channel field and write-only tracker counter; replace map clone. | Existing affected-package tests, then common checks below. Keep report fields and payloads identical. |
| 2 | Move core identity declarations; reuse/move shared query parsing. Avoid the broad error-writer call-site churn in this patch. | Auth and HTTP validation/cursor tests, including duplicate/unknown query parameters and response envelopes. |
| 3 | Move `EventChannel` to `message`, remove the realtime/outbox edge. | Message tests plus cross-replica delivery and outbox integration. Verify exact channel isolation by environment. |
| 4 | Establish one HTTP constructor; migrate all in-repo callers. Narrow the image interfaces at their consumers. | Auth, HTTP, streaming and WebSocket tests. Preserve local omissions, production fail-closed responses, metadata and middleware order. |
| 5 | Inject logger and process-scoped upload admission explicitly. | Busy-before-body-read, stalled multipart, stream/truncation tests; test that handlers sharing a gate share capacity and independent instances do not. Verify logs without `slog.SetDefault` test mutation. |
| 6 | Move focused session tests; add meaningful direct image normalization coverage. Preserve public integration tests. | `go test ./internal/auth ./internal/imageproc` with database fixture where required; existing full integration suite. |
| 7 | Separate version querying from message projection selection. | Schema-1 text send/replay/history/sync/outbox and cleanup; live schema-1 to schema-2 rollout; unknown-version rejection. Do not cache the version at startup. |
| 8a | Extract the shared transaction lock/precondition function with unchanged SQL/order; use it in one send path and then the other. | Account/session rejection, membership hiding/revocation and logout/send concurrency tests on each migration. |
| 8b | Consolidate message/sequence/outbox insertion and replay result handling into the private write operation. Keep image fingerprint/attachment rules explicit. | Concurrent same-key retries, lost-response replay, text/image conflicts, rollback on outbox failure, monotonic room sequence and no sequence gaps. Include image candidate/cleanup integration. |
| 9 | Apply selected responsibility-based file moves and optional error-wrapper deletion in separate patches. | Existing package tests; race tests for realtime. No method-body or locking changes during moves. |
| 10 | Separately fix seed's configuration ownership with an explicit startup behavior change. | Configuration tests prove seed needs database configuration only; local defaults and database errors remain clear. |

After every code change: `gofmt` on changed Go files, `git diff --check`, `go test -count=1 ./...`, `go vet ./...`, and `go build ./cmd/...`. Focused tests may run first to diagnose failures, but do not count integration skips as completion of a step that needs those fixtures. Run `go test -race -count=1` on the affected runtime packages after state/concurrency changes; use the existing Linux CI environment if the local race toolchain is unavailable. Do not broaden tests repeatedly after a clean checkpoint without a new change or unresolved concern.

All behavior-preserving steps retain public URLs, JSON fields, status/error codes, HTTP/WebSocket authentication and rate-limit ordering, schema compatibility, image processing/storage policy, and delivery/lease semantics. Constructor/interface consolidation changes internal exported Go signatures deliberately; no external HTTP/WebSocket redesign is planned.

## Keep rather than abstract away

- `auth.SessionRules`: HTTP and realtime genuinely share eligibility/idle-renewal behavior. Do not merge transaction row locking or refresh-generation fencing into a generic session validator; their atomicity and locking requirements differ.
- Outbox lease tokens, retries, bounded concurrency and batch options: tests exercise stale ownership, duplicate publication and shutdown. These are documented behavior, not speculative flexibility.
- `imagecleanup`: the candidate/deleting/tombstone protocol is the compensation for PostgreSQL and S3 not sharing a transaction. A direct S3 delete wrapper cannot replace it.
- `imageproc`, S3 storage and HTTP streaming: different resource and I/O responsibilities justify their existing separation.
- `httpjson`, `strictjson`, `apierror`, `requestid` and `db`: shared header/envelope/strict-decoding/correlation/config-error policy earns their small size. No catch-all `util` package.
- Direct pgx usage: SQL inside an operation is not itself a layering defect. Extract duplicated business operations, not a generic wrapper around every query.
- Existing manual HTTP route dispatch: swapping to `http.ServeMux` is a separate routing behavior decision because redirects and 404/405 bodies may differ. No route registry/framework is needed for this audit.

## Verification performed for this audit

- `go list` inspected the actual production import graph; packages compile without import cycles.
- Toolchain: Go 1.26.3 on Windows/amd64.
- `go test -count=1 ./...` passed outside the restricted sandbox. The earlier sandbox run failed because its loopback connections were denied; it was not a demonstrated application regression.
- `go vet ./...` passed.
- `go build ./cmd/...` passed.
- `git diff --check` passed before and after report creation (the report is an untracked new file).
- `TEST_DATABASE_URL`, `TEST_VALKEY_URL` and `TEST_IMAGE_ENDPOINT` were unset, so their guarded integration tests did not execute. No database, Valkey, S3, live AWS or race-test success is claimed for this audit.
- Docker's Linux engine named pipe was unavailable. No containers were started or changed.

No Go source, module dependency, deployment or public interface was changed during this audit.
