# 10: Share persisted-session rules across HTTP and WebSocket

**Type:** task

**What to build:** Give HTTP auth and realtime one shared `auth.SessionRules` module for persisted-session eligibility and idle renewal.

**Blocked by:** 04: Stream authorized messages over WebSocket; 05: Add Cognito-backed sessions.

**Status:** claimed

- [ ] Share one eligibility invariant: the user is active; principal user and session IDs match their rows; auth epochs match; the session is not revoked; and absolute and idle expiries are both in the future.
- [ ] Expose focused `Current(ctx, principal)` read-only and `TouchIdle(ctx, principal)` atomic check-and-renew operations. Cap idle renewal at absolute expiry and centralize the existing 30-minute idle period.
- [ ] Centralize the existing 5-second database query bound while honoring caller cancellation.
- [ ] Construct one `auth.SessionRules` value in `cmd/api` and pass it to auth and realtime. Keep cookie/token refresh, send/logout row locks, and refresh lease/generation checks in their current operations.
- [ ] Preserve HTTP 401/503 and WebSocket 4401/1013 outcomes, and keep no-session local test identities on their existing separate path.
- [ ] Keep public HTTP/WebSocket coverage and add focused PostgreSQL tests for eligibility, atomic renewal, cancellation, and database error outcomes.

## Answer

