# Backend load and recovery evidence

This runbook exercises the public HTTP and WebSocket interfaces against an isolated local Compose project. It records measurements; it does not establish an SLA or predict AWS capacity.

## Baseline profile

The default run uses 100 concurrent `chat.v1` WebSocket clients and submits 10 messages per second for 60 seconds. Message text is 256 UTF-8 bytes. Half of the submissions target the seeded group room and half the private room. The current seed has two members in each room, so the group profile is within the M6 ceiling of 20 members. Half of the WebSocket connections subscribe to each room. The two local test identities alternate as senders.

The client measures:

- **Acceptance latency:** time from HTTP `POST /api/v1/rooms/{room_id}/messages` dispatch until a successful `201` or idempotent `200` response.
- **Live delivery latency:** time from that same client-side dispatch until the first matching `message.created` frame reaches each subscribed benchmark socket in the target room. The API does not expose a commit timestamp, so this measure includes HTTP handling and live notification time. Percentiles use one sample per message/socket pair; a socket that receives a duplicate contributes only its first frame to latency.
- **Live missing/duplicate notifications:** the report counts accepted messages with no live frame, expected message/socket pairs with no frame, and repeated frames with the same client message ID on the same socket separately. Partial delivery loss can therefore appear as one or more missing client deliveries while the message still appears in the unique-live-message count. These are observations of this test, not guarantees for all consumers.
- **HTTP sync recovery:** after the load window, the harness walks the public sync pages from the initial `through_seq` snapshot and compares message IDs with accepted requests.
- **Outbox recovery:** the sampler requests one-second intervals and records the actual average interval in the report. A read-only query in the isolated PostgreSQL container samples pending row count and age. After an injected fault, recovery time is the first zero-pending sample after the fault window, provided pending work was observed during that window.
- **Container resources:** Docker CPU and memory samples for running containers in this benchmark project. Sampling can drift because each Docker CLI query takes time; the report records the observed average interval. A stopped container has no sample during its stop interval.

Percentiles use nearest-rank over successful HTTP responses and first live frames per subscribed client. The report includes configured and observed rates separately. Fault scenarios intentionally include a short interruption, so their accepted throughput is expected to be lower than the 10-message/second baseline. `live_client_deliveries` and `live_missing_client_deliveries` count observed and missing message/socket pairs. `recovered_by_sync` counts accepted messages with no live frame on any subscribed socket that are present in the sync result; `not_found_by_sync` counts accepted IDs missing from the sync result. HTTP sync proves the message remains available; it does not simulate each frontend client's cursor repair.

For broker/worker faults, `outbox_recovery_seconds` uses the first zero-pending sample after the fault ended, and is only set when a pending row was sampled during the interruption. `delivery_recovery_seconds` covers observed subscribed-client frames for accepted sends made during the fault window after service restoration. `sync_recovery_seconds` measures fault end to the final sync reconciliation after the configured load window; it includes the remaining run time and does not mean that the frontend automatically syncs at reconnect.

## Isolated environment and commands

`infra/backend/load/compose.benchmark.yaml` uses only project `chat-ticket08-benchmark`, loopback ports 15432 (PostgreSQL), 16379 (Valkey), and 18080 (API), and the named volume `chat-ticket08-benchmark-postgres-data`. It does not reuse the default Compose project or the shared `03-recoverable-outbox-worker-*` stack.

Run a 60-second baseline:

```powershell
.\infra\backend\load\run.ps1 -Scenario baseline
```

Run API restart, worker restart, or Valkey interruption scenarios:

```powershell
.\infra\backend\load\run.ps1 -Scenario api-restart -DurationSeconds 30
.\infra\backend\load\run.ps1 -Scenario worker-restart -DurationSeconds 30
.\infra\backend\load\run.ps1 -Scenario valkey-interruption -DurationSeconds 30
```

By default, each fault run stops the selected service 12 seconds after all WebSocket subscriptions are ready, leaves it stopped for 5 seconds, starts it again, and allows a post-recovery interval. The recorded fault runs below used an 8-second delay and a 5-second interruption. API clients reconnect after a socket closes using a 250 ms exponential backoff capped at 3 seconds. This is load-harness behavior only; it does not describe or implement the separately owned frontend.

Run all scenarios with `-Scenario all`. Change payload, traffic mix, connection count, rate, or duration with the corresponding script parameters; use 100 connections and 10 messages/second when collecting baseline evidence. The report also stores the prepared room sizes. The harness validates the group size is at most 20 but does not mutate the fixture.

Each run writes a JSON report and its readiness/fault timestamps under `docs/adr/evidence/`. A stderr log is kept for diagnosing failed runs. Reports include exact times, workload settings, latency distributions, accepted and attempted messages, HTTP errors, WebSocket disconnect/reconnect counts, notifications, sync results, outbox backlog and resource samples.

Remove only the isolated benchmark project and its database volume after preserving the JSON evidence:

```powershell
.\infra\backend\load\run.ps1 -Scenario cleanup
```

This command is scoped to the project and explicit benchmark Compose file above. Do not use the root `docker compose down -v` command as part of this benchmark.

## Measured results

The recorded environment was a Windows host using Docker Desktop with a Linux x86_64 engine (Docker Engine 29.8.1, Compose 5.5.1, 12 Docker CPUs and 8,194,240,512 bytes of Docker VM memory). Go 1.26.0 generated the clients. Each scenario used one API container, one worker container, PostgreSQL 17 Alpine and Valkey 8 Alpine; Compose set no per-container CPU or memory limits. The API log reported a 12-connection PostgreSQL pool. The prepared private and group rooms each had two members. There was no AWS region or cloud service in these measurements.

The baseline was a 60-second run. Fault runs lasted 30 seconds, with the service stopped for 5 seconds starting 8 seconds after the 100 sockets had subscribed.

| Scenario | Attempts / accepted | Acceptance p50 / p95 / p99 (ms) | Submit to subscribed-client live frame p50 / p95 / p99 (ms) | Delivery and recovery observations |
| --- | ---: | ---: | ---: | --- |
| Baseline | 600 / 600 (10.00/sec) | 3.76 / 6.48 / 23.82 | 139.13 / 240.86 / 245.71 | 100 sockets and 30,000 observed message/socket deliveries; zero HTTP errors, missing or duplicate frames; sync found all accepted messages. |
| Worker restart | 300 / 300 (10.00/sec) | 3.60 / 5.75 / 13.69 | 169.97 / 4,888.11 / 6,172.92 | Pending outbox peaked at 50, returned to zero 2.71 sec after restart; last observed client delivery for messages submitted during the fault was 0.036 sec after restoration. No missing client deliveries or duplicate frames. |
| API restart | 300 / 237 (7.90 accepted/sec) | 3.90 / 7.65 / 9.58 | 129.77 / 231.82 / 234.42 | 63 HTTP transport errors during the outage; all 100 sockets reconnected. Four accepted messages had no live frame (200 expected message/socket deliveries missing); all four messages were present in final HTTP sync; zero accepted IDs were missing from sync. |
| Valkey interruption | 300 / 300 (10.00/sec) | 3.71 / 5.95 / 12.45 | 119.96 / 221.25 / 225.45 | Pending outbox peaked at 79 and returned to zero 10.84 sec after restoration. 95 accepted messages lacked any live frame (4,750 expected message/socket deliveries missing) and all 95 appeared in HTTP sync; zero duplicate frames. |

Across the baseline's sampled resource observations, maximum CPU was 10.11% for API, 14.61% for PostgreSQL, 2.69% for Valkey and 2.49% for worker. Maximum sampled memory was 19.2 MiB, 76.0 MiB, 9.2 MiB and 9.4 MiB respectively. The sampler requested one-second intervals but measured 3.1 seconds average because Docker CLI probes took longer; fault-run resource samples and timing are in each JSON report. These are container observations on the local Docker VM, not resource limits or service sizing recommendations.

The raw reports are `docs/adr/evidence/ticket-08-baseline.json`, `ticket-08-worker-restart.json`, `ticket-08-api-restart.json` and `ticket-08-valkey-interruption.json`. The 10-second connection probe is supplementary. Interpret every result as evidence from this local machine and these container versions only. The baseline target is 100 sockets, 10 aggregate messages per second and a group size no greater than 20. The stretch target is 1,000 sockets and 100 messages per second; it is not a capacity claim and was not run.

No duplicate notification was observed in these real-service load runs. Ticket03's separate forced-duplicate integration test, `TestRetryAfterLostPublishAcknowledgementCanDuplicateTheEvent` in `internal/outbox/worker_integration_test.go`, creates a message through public HTTP, observes Valkey Pub/Sub through a subscriber, simulates a lost publish acknowledgement after Valkey accepted the event, retries the outbox row, receives the repeated event, and verifies HTTP sync still resolves to one durable message. Ticket03 records three consecutive passes of the failure-window tests. Ticket08's public harness test also injects a dropped frame and a repeated frame in a fake HTTP/WebSocket endpoint to verify that the report counts message/socket delivery gaps and duplicate frames separately; this is harness-accounting coverage, not a measured service duplicate.

No AWS account was used. RDS Multi-AZ failover, ECS autoscaling, task sizing, ALB behavior, regional latency, and AWS resource cleanup remain **unverified**. The local stack cannot prove those behaviors.
