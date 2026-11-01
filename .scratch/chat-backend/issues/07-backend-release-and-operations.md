# 07: Release and operate backend services safely

**What to build:** Operators can build immutable API and worker artifacts, run migrations once, roll backend tasks safely, observe failures, and recover without losing committed messages.

**Blocked by:** 05: Add Cognito-backed sessions; 06: Define backend AWS runtime infrastructure.

**Status:** ready-for-agent

- [ ] API and worker images identify the tested source/artifact by immutable digest.
- [ ] CI checks and builds the backend; deployment uses a controlled migration job rather than auto-migrating every API replica.
- [ ] API readiness/drain and worker shutdown release requests, sockets, and leases within configured grace periods.
- [ ] The release procedure supports rolling deployment and rollback while preserving old/new schema compatibility.
- [ ] Backend dashboards/alarms cover request errors/latency, socket health, outbox age/retries, dependency health, CPU/memory, and database connection pressure.
- [ ] Logs carry request correlation IDs and omit message bodies, credentials, cookies, and session tokens.
- [ ] Unavailable CI/AWS environment is reported as unverified; no deployment success is inferred from local builds.
