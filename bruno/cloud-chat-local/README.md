# Cloud Chat local Bruno collection

Open this folder as a collection in Bruno and select the `local` environment.
Start the API and worker using the local setup in the repository README first.

Start all three dependencies with `docker compose up -d --wait postgres valkey media`, then run migrations and seed before starting API and worker. Bruno does not create rooms. Go does not load `.env` automatically; set the shell environment as shown in the repository README. This collection expects schema 2 for image requests and image sending enabled (the local default).

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

## Image flow

Run these HTTP requests in order, after 01–07:

| Request | Expected behavior |
| --- | --- |
| 09 send image with caption | 201 new / 200 replay; saves the returned image URL and message ID as runtime variables |
| 10 retry image | 200 replay of the same message |
| 11 changed caption | 409 idempotency conflict |
| 12 read image as Demo B | 200 private PNG, no-store/nosniff |
| 13 image without caption | 201 new / 200 replay with empty text |
| 14 history | Includes the image metadata |
| 15 sync as Demo B | Includes the image metadata from the sequence captured by 09 |
| 16 reuse image ID for JSON text | 409 idempotency conflict |
| 17 read without identity | 401 |

The bundled `fixtures/image.png` is an 8×8 PNG with alpha. File paths are relative to the collection. Let Bruno generate the multipart Content-Type and boundary; do not add that header manually. The request mode is `multipartForm`, and the body block is `body:multipart-form`.

Environment variables `imageClientMessageId`, `imageOnlyClientMessageId`, and `imageCaption` control the image cases. Requests 09 and 10 must use identical files and captions. Keep IDs for retry; use fresh UUIDs for new messages. If changing the fixture, update its width/height assertions in request 09. Run 09 before any request using `sentImageUrl` or `imageBeforeSeq`; those runtime variables are deliberately not hardcoded in the environment.

Connect WebSocket 08 separately before running 09 to inspect `message.created`. It carries metadata/URL only. For a DM, set `groupRoomId` to a seeded DM room ID shared by Demo A/B; the backend uses the same contract. Do not include WebSocket 08 in an automated HTTP collection run.

Uploads are bounded to 5 MiB (6 MiB total multipart), one pipeline per API task, with a 45-second body-read deadline. On 429 `IMAGE_UPLOAD_BUSY`, wait the Retry-After value and retry the same ID/file/caption. A response lost after commit may also require an identical retry. Downloads interrupted midstream should retry only GET. Oversize files, deliberate slow uploads, and member removal are covered by backend tests or manual fixtures, not by the small bundled image.

See [image flow and rollout documentation](../../docs/image-messages.md) and [shared edge requirements](../../infra/shared-edge/README.md). Bruno's local adapter does not validate production cookies, CSRF, CloudFront, or AWS IAM.

From this collection directory, run the HTTP requests with the pinned CLI (WebSocket 08 is excluded):

```powershell
$httpRequests = (Get-ChildItem -File *.bru | Where-Object Name -NotLike '08-*' | Sort-Object Name).Name
npm.cmd exec --yes --package=@usebruno/cli@4.1.0 -- bru run @httpRequests --env local --bail
```

Offline syntax checks, from the repository root:

```powershell
npm.cmd exec --yes --package=@usebruno/cli@4.1.0 -- node .scratch/image-messages/validate-bruno.cjs
```

All 17 request files and the environment were parsed using Bruno 4.1.0; embedded JavaScript and fixture paths were validated. The updated Bruno HTTP flow has not yet been executed against the API because the Docker engine endpoint was unavailable during this documentation update. This is separate from the passing Go/backend integration evidence in ticket 03.
