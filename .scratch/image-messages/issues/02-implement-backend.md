# 02 — Implement backend image messages

Type: task
Status: resolved
Blocked by: 01

## Goal

Implement the accepted backend image-message contract in [spec](../spec.md). Keep the frontend a separate owner's responsibility. Do not apply AWS infrastructure or claim cloud verification.

## Acceptance

- JPEG/PNG/WebP multipart sends up to 5 MiB with optional caption; text-only API behavior stays compatible.
- Inspect, bound, orient, resize, and strip metadata; store processed images privately and add authenticated member-only reads.
- Preserve idempotency, room order, history/sync, outbox and realtime; safe retry, candidate cleanup and migrations.
- Shared local S3-compatible storage; AWS bucket/IAM Terraform where feasible in backend infrastructure scope.
- Implement meaningful tests; run required Go checks and local integration if Docker works.
- Update API/schema/README/docs and resolve this ticket with evidence and caveats.

## Answer

Implemented multipart image sends, image processing and private storage, authenticated member-only image reads, message metadata through history/sync/outbox/realtime, migration 0002, and cleanup through the existing worker. Added a production feature flag that stays off until the API and worker rollout is complete; local development defaults on. Added the private AWS bucket and scoped API/worker IAM, plus shared local SeaweedFS storage.

`go build ./...` succeeds. Compose integration, actual local S3-compatible storage behavior, and AWS deployment were not run. The frontend remains a separate owner's work.

## Comments

Local rollout/storage integration and runtime hardening were subsequently completed in [ticket 03](03-harden-image-runtime.md). AWS deployment remains out of scope.
