# 04 — Update Bruno and image documentation

Type: task
Status: resolved
Blocked by: 03

## Goal

Add executable image requests and a bundled fixture to Bruno; document the implemented flow, runtime bounds, rollout and verification limits.

## Answer

Added requests 09–17 for captioned/image-only sends, replay, caption/text-mode conflicts, protected member/unauthenticated downloads, history and sync. Included an 8×8 alpha PNG fixture, image environment variables and response-derived runtime URLs/sequence values. Existing text and WebSocket requests now use environment variables consistently.

Added docs/image-messages.md and updated API links, ADR implementation status, codebase map, root README, and Bruno setup/CLI instructions. Documentation reflects the 45-second upload deadline, 429/408 handling, streaming downloads, schema-1 compatibility rollout and shared-edge integration requirements.

Validated all 17 requests and the environment with the actual Bruno 4.1.0 parser, compiled JavaScript blocks, checked fixture paths/dimensions and ran git diff --check. The updated Bruno flow was not executed against the API: Docker's engine endpoint was unavailable, and Docker was not started. The existing backend integration results are recorded separately in ticket 03. No AWS changes.
