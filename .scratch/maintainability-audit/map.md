# Maintainability refactoring

## Notes

- Architectural audit and target plan: [spec.md](spec.md).
- Completed increment: [01: Low-risk cleanup](issues/01-low-risk-cleanup.md).
- Completed increment: [02: Constructor and interface consolidation](issues/02-constructor-and-interface-consolidation.md).
- Completed increment: [03: Explicit runtime ownership](issues/03-explicit-runtime-ownership.md).

## Decisions-so-far

- Keep the existing packages and explicit pgx operations. Start with deletion and correction of ownership before changing message transactions.
- The initial four cleanup checkpoints passed the full test suite with isolated PostgreSQL, Valkey and S3-compatible storage, plus vet/build/format/diff checks after every checkpoint. Details: [01](issues/01-low-risk-cleanup.md).
- [01](issues/01-low-risk-cleanup.md) is resolved. A final race pass succeeded for auth, HTTP, realtime, outbox and loadbench; the disposable test project was removed. The user's pre-existing codebase-map edits were verified unchanged by SHA-256.
- [02](issues/02-constructor-and-interface-consolidation.md) is resolved. One HTTP constructor with plain `Options` replaces the four entry points; image interfaces now belong to HTTP (Put/Get) and cleanup (Delete). Both checkpoints passed the full configured integration suite, vet, builds, formatting and diff checks. Optional dependency and production fail-closed regression coverage was added, and the disposable test project was removed.
- [03](issues/03-explicit-runtime-ownership.md) is resolved. Request logging captures an explicit logger, and `cmd/api` owns one capacity-one upload channel passed to HTTP. Shared-gate contention, independent admission, failure release, body-read rejection and logger isolation are covered. Both checkpoints passed the full configured suite/static checks, followed by the HTTP race suite; the disposable test project was removed.

## Fog

- Focused image/session coverage, schema projection ownership and message transaction consolidation remain later increments in the audit plan.
- The existing CI image-storage fixture gap remains; local integration coverage now executes using the existing Compose fixture. No CI workflow was changed in this cleanup increment.
