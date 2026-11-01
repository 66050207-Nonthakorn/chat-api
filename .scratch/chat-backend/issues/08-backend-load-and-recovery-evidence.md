# 08: Measure backend load and recovery

**Type:** task

**What to build:** The team can reproduce backend load and failure scenarios and report measured limits, latency, errors, and recovery behavior without claiming capacity that was not measured.

**Blocked by:** 07: Release and operate backend services safely.

**Status:** resolved

- [x] A repeatable scenario reaches the baseline target of 100 WebSocket connections and 10 messages per second with documented payload, room size, duration, and environment.
- [x] Results report p50/p95/p99 acceptance and live-delivery latency, errors, resource use, and outbox recovery time.
- [x] Scenarios cover API/worker restart, broker interruption, reconnect, backlog recovery, and duplicate/missing notifications.
- [x] RDS failover and autoscaling are tested only when account permissions, credits, and time permit; otherwise report them as unverified.
- [x] The report separates measured results from stretch targets and includes cleanup evidence for any temporary AWS resources.

## Answer

Implemented in `6ee28d3` and `64ff1d9`, then merged into `integration` at `64ff1d9`.

Verification: the repeatable local Compose harness reached 100 subscribed sockets and 10 accepted messages/second for 60 seconds: 600/600 accepted, with 30,000 observed message/socket deliveries and no missing or duplicate frames. Acceptance latency was p50/p95/p99 3.76/6.48/23.82 ms; submit-to-subscribed-client live delivery was 139.13/240.86/245.71 ms. Worker restart drained a 50-row backlog 2.71 seconds after restart. API restart had 63 transport errors during the outage; all 100 sockets reconnected, and all four messages without live frames were present in HTTP sync. Valkey interruption peaked at 79 pending rows, drained in 10.84 seconds, and all 95 messages without live frames were recovered by HTTP sync. The duplicate-publication fault test is documented separately from observed benchmark duplicates.

The report and runbook are in `docs/adr/evidence/` and `docs/adr/references/08-backend-load-and-recovery-evidence.md`. Measurements are local Docker-only, with resource sample interval and container limits documented. The isolated benchmark project and volume were removed; shared ticket03 PostgreSQL/Valkey services remain healthy. AWS RDS failover, autoscaling, ALB behavior, and AWS cleanup remain unverified.
