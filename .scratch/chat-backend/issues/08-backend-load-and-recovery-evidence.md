# 08: Measure backend load and recovery

**What to build:** The team can reproduce backend load and failure scenarios and report measured limits, latency, errors, and recovery behavior without claiming capacity that was not measured.

**Blocked by:** 07: Release and operate backend services safely.

**Status:** ready-for-agent

- [ ] A repeatable scenario reaches the baseline target of 100 WebSocket connections and 10 messages per second with documented payload, room size, duration, and environment.
- [ ] Results report p50/p95/p99 acceptance and live-delivery latency, errors, resource use, and outbox recovery time.
- [ ] Scenarios cover API/worker restart, broker interruption, reconnect, backlog recovery, and duplicate/missing notifications.
- [ ] RDS failover and autoscaling are tested only when account permissions, credits, and time permit; otherwise report them as unverified.
- [ ] The report separates measured results from stretch targets and includes cleanup evidence for any temporary AWS resources.
