# User search, direct messages, and group invites

Proposed design for the single-community chat app. Phase 1 ships user search and DMs together. Phase 2 reuses the directory for group creation and invitations. Both phases use existing room listing, message history, sync, and WebSocket subscriptions.

[Identity and membership rules](../../docs/adr/0002-identity-sessions-and-membership.md) · [Current API](../../docs/api-contract.md) · [Current schema](../../internal/migrations/sql/0001_initial.sql)

## Shared API rules

Require an app session on each endpoint and use existing exact-origin and CSRF checks on writes. Take the acting user from the session. Use the existing error envelope and request validation. Apply the shared read limit to directory and invite lists. Put room creation and invite writes in a separate shared per-user rate-limit bucket. Return 429 with Retry-After when limited.

Return the existing Room shape from room creation and invite acceptance. Its membership_role is relative to the caller and last_seq remains a decimal string. Commit before sending a success response.

## Phase 1 · Find people and start a DM

### User search

`GET /api/v1/users?query=ann` returns `{"items":[{"user_id":"<uuid>","display_name":"Ann"}]}`.

- Search active app users other than the caller by case-insensitive display-name substring. Trim the query and require 2–100 Unicode characters. Treat SQL wildcard characters literally and bind query parameters.
- Return up to 20 matches ordered by case-insensitive display name and then user ID. Ask users to refine broad searches. Use the UUID for selection even when names match. Email addresses and Cognito identifiers stay private.
- Add a **New message** button to the sidebar. Debounce search and discard responses for superseded queries. Show a prompt, empty state, and retryable error in the picker.

### Open a DM

`POST /api/v1/dms` accepts `{"peer_user_id":"<uuid>"}`. Return 201 for a new DM or 200 with the existing DM. Reject self-DMs with 400 and return the same 404 for an unknown or inactive peer.

- Canonicalize the unordered pair into `dm_user_low` and `dm_user_high`. Use the existing unique pair index to arbitrate concurrent requests. In one transaction, create the room and both member rows. On a pair conflict, read the winning room in a subsequent statement. A failed membership insert rolls back creation. Repeat requests, including from the other participant, resolve to the same room.
- Project the other participant’s display name as the DM title in both the creation response and room listing. Keep the stored title valid for the existing schema. Group titles still come from the room.
- Upsert the returned room into the sidebar and open it using the existing selection and subscription flow. Keep an explicit **Refresh conversations** action and refresh the first page when the app regains focus so the peer can discover the DM. Keep existing pagination and deduplicate by room ID when merging newly created rooms. Discovery uses the room list since the current socket delivers events for rooms the client has subscribed to.

### Phase 1 acceptance

- Search returns only eligible users and handles duplicate names and literal wildcard input.
- Simultaneous A-to-B and B-to-A requests and retries after a lost response yield one room with exactly two memberships. Each participant sees the other’s name. A third user cannot list, read, send to, or subscribe to the DM.
- In two browser sessions, A starts a DM, B refreshes conversations, and both can open the same room, exchange messages, and recover history after reconnecting.

## Phase 2 · Create a closed group and invite people

### Create a group

`POST /api/v1/rooms` accepts `{"room_id":"<client-generated UUID v4>","title":"Project Chat"}`. Trim the title and require 1–200 Unicode characters. The frontend generates the ID once per submission and keeps it for retries.

Create the group and creator membership in one transaction. Set `rooms.owner_user_id` to the creator and store their membership as `member`, which the room projection exposes as `owner`. Return 201 for creation or 200 when the ID already belongs to the same owner’s group with the same title. Return 409 on a conflicting ID. The frontend opens the group and offers **Invite people**.

### Invite and respond

| Method and path | Contract |
| --- | --- |
| `POST /api/v1/rooms/{room_id}/invites` | Body `{"invitee_user_id":"<uuid>"}`. A current group owner or admin creates a pending invite. Return 201 with the invite or 200 with the existing pending invite for the same room and recipient. Return 409 if the recipient is already a member. |
| `GET /api/v1/invites` | Return the caller’s pending incoming invites in `{items,next_cursor}`, newest first with an invite ID tie-breaker. Default limit 20, max 50. Each item contains invite_id, room_id, room_title, inviter as a user summary, and created_at. |
| `POST /api/v1/invites/{invite_id}/accept` | Only the recipient can accept. Atomically record acceptance and insert a regular member row. Return 200 with the Room. A repeated acceptance returns the Room while the recipient remains a member. |
| `POST /api/v1/invites/{invite_id}/decline` | Only the recipient can decline. Record the decline and return 204. Repeating a decline also returns 204. |

- Add a PostgreSQL group invite table with an invite ID, room, inviter and invitee IDs, status, created_at, and resolved_at. Use a partial unique index for one pending invite per room and recipient, plus an inbox index for the invitee and page order. Invites belong to the group after issuance and stay pending until answered in this first version.
- Serialize invite creation and response for the same room by locking the room row before reading or writing invite and membership state. Lock or otherwise serialize reads of the inviter’s membership against role changes before granting authority to issue the invite. Verify the invitee is active when issuing the invite. For decisions, verify recipient ownership, then transition from pending exactly once. Opposing decisions return 409 once resolved. Replaying an accepted invite after membership removal returns 409 and cannot recreate access.
- Treat inaccessible rooms and another user’s invite IDs as 404. A regular group member attempting to invite gets 403. A DM cannot issue an invite. Self-invites are invalid. Inactive and unknown recipients share a 404.
- Add an **Invitations** inbox with Accept and Decline. Load on open and refresh on focus. An invite exposes the group title and inviter name to its recipient. The recipient receives no room access until acceptance commits. Explain before acceptance that group membership grants access to the full history, including images.
- Keep the first release to in-app invitations for provisioned users. External onboarding, invitation expiration and cancellation, room renaming, and membership administration UI are separate work.

### Phase 2 acceptance

- The creator immediately sees the group as owner. An admin can invite, a regular member cannot, and a DM rejects invitations.
- Concurrent sends to the same recipient produce one pending invite. A different user cannot list or decide it. Concurrent accept and decline produce one terminal decision. A declined invite never grants membership.
- Before acceptance, the recipient cannot read history, load images, send, or subscribe. After acceptance, the room opens and its full history is available. An acceptance replay cannot restore a removed membership.
- With two browser sessions, the owner creates a group and invites a user. The recipient accepts through the inbox, sees the room in their sidebar, and exchanges messages using the existing chat flow.
