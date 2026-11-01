# 05: Add Cognito-backed sessions

**What to build:** An invited user can authenticate through Cognito while the backend keeps provider tokens out of browser JavaScript and enforces revocation, CSRF, and Origin rules across replicas.

**Blocked by:** 04: Stream authorized messages over WebSocket.

**Status:** ready-for-agent

- [ ] Login and callback use one-time state, nonce, browser binding, and PKCE; callback maps a verified issuer/subject only to a prepared internal user.
- [ ] Browser receives only an opaque Secure, HttpOnly, SameSite cookie; provider tokens remain encrypted on the server.
- [ ] Session idle and absolute expiry, logout, logout-all, refresh coordination, and socket revalidation follow the contract.
- [ ] State-changing requests enforce exact Origin and a session-bound CSRF token.
- [ ] Local mock identity cannot be enabled in AWS configuration.
- [ ] Auth routes are tested at the public HTTP seam using a fake OIDC provider; live Cognito verification is tracked separately as an external prerequisite.
