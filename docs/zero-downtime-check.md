# Deployment availability check

Continuously request the public API readiness endpoint before, during, and after a backend rollout. Record timestamps, HTTP status codes, and timeouts, then check for failed requests. Keep a WebSocket client connected too, and verify that it reconnects and recovers messages through sync when a task drains.

A merge to `main` runs CI and publishes images after verification. The deployment operator must apply the published image digests to roll out the release. Watch the ECS deployment and CloudWatch error and latency metrics while the rollout runs. See [AWS deployment and operations](../infra/README.md) for the release procedure.
