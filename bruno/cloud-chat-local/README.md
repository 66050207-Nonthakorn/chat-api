# Cloud Chat local Bruno collection

Open this folder as a collection in Bruno and select the `local` environment.
Start the API and worker using the local setup in the repository README first.

The HTTP smoke flow is:

1. `01-health-live`
2. `02-health-ready`
3. `03-list-rooms-as-demo-a`
4. `04-send-message-as-demo-a`
5. `05-retry-message-same-id`
6. `06-sync-as-demo-b`
7. `07-read-history-as-demo-a`

Run `08-websocket-subscribe-demo-group` separately. Connect it, then send a
message through `04-send-message-as-demo-a` as the other Bruno request or from
a second Bruno window. The WebSocket request includes the local Origin,
subprotocol, and demo identity headers required by the backend.

This collection uses the local-only `X-Local-Test-User-ID` adapter. It does not
exercise Cognito login or production session cookies, and the adapter must not
be enabled outside local development.
