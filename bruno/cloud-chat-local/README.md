# Cloud Chat local Bruno collection

Open this folder as a collection in Bruno and select the `local` environment.
Start the API and worker using the local setup in the repository README first.

Start all three dependencies with `docker compose up -d --wait postgres valkey media`, then run migrations and seed before starting API and worker. Go does not load `.env` automatically; set the shell environment as shown in the repository README. This collection expects schema 3 and image sending enabled (the local default).

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

## DM and group management

Requests 18–26 need an additional active user as a DM recipient. After running the regular seed, create this local fixture once from the repository root:

```powershell
docker compose exec -T postgres psql -U chat -d chat -c "INSERT INTO app_users (user_id, display_name) VALUES ('55555555-5555-4555-8555-555555555555', 'Demo C') ON CONFLICT (user_id) DO NOTHING;"
```

This fixture is only a recipient; local test authentication remains limited to Demo A and Demo B. The first run creates a DM between A and C; later runs reopen the same DM. Run requests 18–26 in order in the same Bruno session:

| Request | Expected behavior |
| --- | --- |
| 18 search Demo C | Finds the active recipient in the directory |
| 19–20 start and reopen DM | 201 on first creation or 200 if it exists; retry returns the same room |
| 21–22 create and retry group | Creates an owner-only group or replays the original creation; saves its room ID |
| 23–24 add and retry member | Adds Demo B and replays the same request without adding another membership |
| 25 list members as Demo B | Shows Demo A as owner and Demo B as a member |
| 26 member cannot add | Demo B cannot add Demo C; returns 404 |

The group room ID and DM room ID are runtime variables saved by 21 and 19; rerun those requests before dependent requests after restarting Bruno. Keep `groupCreateRequestId`, `groupAddRequestId`, and their request bodies unchanged for retries. To create a separate group, generate fresh UUIDs for both IDs. Changing the title while reusing the creation ID yields a 409 conflict. Existing group membership is retained between runs; the collection does not remove users or delete groups.

From this collection directory, run the HTTP requests with the pinned CLI (WebSocket 08 is excluded):

```powershell
$httpRequests = (Get-ChildItem -File *.bru | Where-Object Name -NotLike '08-*' | Sort-Object Name).Name
npm.cmd exec --yes --package=@usebruno/cli@4.1.0 -- bru run @httpRequests --env local --bail
```

Offline syntax checks, from the repository root:

```powershell
npm.cmd exec --yes --package=@usebruno/cli@4.1.0 -- node .scratch/image-messages/validate-bruno.cjs
```

The offline check parses the requests and environment and validates embedded JavaScript and fixture paths. It does not execute API calls.
