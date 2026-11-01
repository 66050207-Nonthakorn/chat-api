# 07: Release and operate backend services safely

**What to build:** Operators can build immutable API and worker artifacts, run migrations once, roll backend tasks safely, observe failures, and recover without losing committed messages.

**Blocked by:** 05: Add Cognito-backed sessions; 06: Define backend AWS runtime infrastructure.

**Status:** resolved

- [x] API and worker images identify the tested source/artifact by immutable digest.
- [x] CI checks and builds the backend; deployment uses a controlled migration job rather than auto-migrating every API replica.
- [x] API readiness/drain and worker shutdown release requests, sockets, and leases within configured grace periods.
- [x] The release procedure supports rolling deployment and rollback while preserving old/new schema compatibility.
- [x] Backend dashboards/alarms cover request errors/latency, socket health, outbox age/retries, dependency health, CPU/memory, and database connection pressure.
- [x] Logs carry request correlation IDs and omit message bodies, credentials, cookies, and session tokens.
- [x] Unavailable CI/AWS environment is reported as unverified; no deployment success is inferred from local builds.

## Resolution

Implemented in `5b814f0`, `26f394b`, and `4819fd3`, then merged into `integration` at `4819fd3`.

Verification: `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, and `go build ./cmd/...` passed against the shared PostgreSQL/Valkey stack. API/worker/migration Docker targets built; the final clean-commit image builder verified source revision labels and local image IDs. Terraform format, validate, and the non-applying placeholder plan passed with 70 additions. PowerShell release scripts were checked. CI and AWS environments were not run: no image was pushed, deployed, or applied, and CloudWatch alarms were not evaluated. OpenTelemetry traces are deferred and not claimed as implemented.
