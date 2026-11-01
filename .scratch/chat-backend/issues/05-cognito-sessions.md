# 05: Add Cognito-backed sessions

**What to build:** An invited user can authenticate through Cognito while the backend keeps provider tokens out of browser JavaScript and enforces revocation, CSRF, and Origin rules across replicas.

**Blocked by:** 04: Stream authorized messages over WebSocket.

**Status:** resolved

- [x] Login and callback use one-time state, nonce, browser binding, and PKCE; callback maps a verified issuer/subject only to a prepared internal user.
- [x] Browser receives only an opaque Secure, HttpOnly, SameSite cookie; provider tokens remain encrypted on the server.
- [x] Session idle and absolute expiry, logout, logout-all, refresh coordination, and socket revalidation follow the contract.
- [x] State-changing requests enforce exact Origin and a session-bound CSRF token.
- [x] Local mock identity cannot be enabled in AWS configuration.
- [x] Auth routes are tested at the public HTTP seam using a fake OIDC provider; live Cognito verification is tracked separately as an external prerequisite.

## Resolution

Implemented in `5215b14` and `2f9944e`, then merged into `integration` at `2f9944e`.

Verification: fake-OIDC public HTTP tests and PostgreSQL/Valkey auth/WebSocket integration tests passed, along with `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, and `go build ./cmd/...`. Coverage includes PKCE, state/nonce/browser binding and replay, invited issuer-subject mapping, encrypted tokens, opaque cookies, expiry, CSRF/Origin, logout/logout-all, refresh races, and socket revalidation after revocation. Live Cognito and AWS apply were not run.

Production OIDC settings: `APP_ORIGIN`, `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, `SESSION_ENCRYPTION_CURRENT_KEY_ID`, and `SESSION_ENCRYPTION_KEYS`. Only `OIDC_CLIENT_SECRET` and `SESSION_ENCRYPTION_KEYS` are secret values. The key ring is JSON mapping key IDs to base64-encoded 32-byte keys; the current key ID selects new encryption while older keys remain available for decryption during rotation.
