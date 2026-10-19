# Local API and MCP

This release implements local account management, exclusions, event history,
recipient previews, standalone refresh, confirmed friend synchronization, paginated
conversations, text history, direct/group texts and snaps, encrypted media uploads,
guarded My Story publishing, incoming requests, public identity lookup, and own
story status reads. See [validation and limits](STANDALONE.md).

## Run

```sh
cd snapchat-mcp
make build
./bin/snapchat-mcp serve
```

The default origin is `http://127.0.0.1:8787`. Data lives in
`$XDG_CONFIG_HOME/qq_snap` (normally `~/.config/qq_snap`): `service.db` and `token`.
The directory is private and both files use owner-only permissions. Use
`--data-dir` to choose a separate installation. The service accepts literal
loopback listen IPs only. HTTP requires its generated bearer token and rejects
browser Origin headers. Do not publish this listener through a reverse proxy.

Provision the OS keyring master key explicitly when credential storage is needed:

```sh
./bin/snapchat-mcp serve --init-keyring
```

The vault uses AES-256-GCM with the account ID bound as authenticated data. No
plaintext credential fallback is implemented. Normal local metadata operations
work without a desktop keyring. Browser session capture is available through the CLI:

```sh
./bin/snapchat-mcp bootstrap --account ACCOUNT_ID --init-keyring
```

Run it with the separate Chromium profile signed in on CDP port 9222. Tokens,
cookies, wrapped identity records, and the current account’s root wrapping key
are captured directly into the vault; their values are never printed. A local
account cannot be rebound to another Snapchat identity. Browser capture does
not initialize or replace device keys.

Standalone refresh is `POST /v1/accounts/{account_id}/accounts/refresh` with `{}`.
It returns status and timestamps, never tokens. A live test succeeded with
Chromium closed after the service had restarted. Full protocol coverage remains false independently of authentication.

## Account and exclusion example

```sh
API=http://127.0.0.1:8787
TOKEN=$(cat "${XDG_CONFIG_HOME:-$HOME/.config}/qq_snap/token")
curl -sS -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"label":"personal"}' "$API/v1/accounts"
```

Use `data.id` from the response as `ACCOUNT_ID`. Creating an account leaves it in
`protocol_blocked` state; no Snapchat login or outbound activity occurs.

```sh
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$API/v1/accounts/$ACCOUNT_ID/capabilities/list"

./bin/snapchat-mcp import-exclusions --account "$ACCOUNT_ID" \
  --file $HOME/.config/qq_snap/exclusions.json

curl -sS -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"recipients":["friend-uuid","excluded-uuid"]}' \
  "$API/v1/accounts/$ACCOUNT_ID/jobs/preview"
```

The import merges exclusions into the specified account and never sends anything.
Import additional muted IDs as a separate JSON array if they should also be
excluded. Nothing is assigned to an account implicitly. HTTP
`exclusions/replace` replaces the complete list, including clearing it with `[]`.
Previews deduplicate IDs and apply local exclusions, but cannot verify current
friendships or submit batch jobs.

## Interface conventions

- `GET /v1/accounts` lists accounts; `POST /v1/accounts` registers one.
- `GET /v1/accounts/{account_id}` returns local account status.
- Other operations use `/v1/accounts/{account_id}/{family}/{operation}`.
  Reads use GET; writes use POST. `jobs/preview` and the message, snap, and
  story preparation operations use POST for their inputs; they never submit
  messages or publish content.
- Responses contain `data` or `error: {code, message}`. An account ID in a body or
  query is rejected; HTTP gets it exclusively from the URL. MCP requires it as
  an explicit argument.
- Local list pagination uses `limit` (1–500) and an opaque `cursor`; page responses use
  `items` and optional `next_cursor`. Event cursors are monotonically increasing
  local event IDs. Poll again with the last received event ID even when a page
  has no next cursor.
- HTTP event streaming: `GET /v1/accounts/{account_id}/events/stream`. Resume with
  `Last-Event-ID` or `?cursor=…`. Event payloads are account-scoped local changes;
  native Snapchat event ingestion remains unavailable.
- Unknown fields, wrong types, missing required arguments, duplicate query
  parameters, and oversized JSON requests are rejected. Request bodies are
  limited to 1 MiB, except `media/upload`, which permits 24 MiB JSON bodies for
  up to 16 MiB of decoded media.
- `GET /v1/health` reports service health separately from protocol readiness.
- `GET /v1/openapi.json` serves the generated OpenAPI 3.1 document, including
  preliminary unavailable operations. `./bin/snapchat-mcp api-spec` exports it offline.

## Capability matrix

| Feature | Status |
| --- | --- |
| Account registration/list/status | Available locally |
| Exclusion list/replace/explicit merge import | Available locally |
| Durable event polling and HTTP streaming | Available for local changes |
| Recipient preview | Available; no friendship verification |
| Job list/get/cancel infrastructure | Available; no new job submission |
| OS keyring-backed encrypted credential vault | Available; per-account authenticated encryption |
| Browser-assisted credential capture | Available through `bootstrap` |
| Standalone session refresh | Available and validated with Chromium closed |
| Offline identity-key restoration | Validated with version-10 captured key; no registration or network |
| Native messaging session initialization | Validated offline and online |
| Confirmed friend synchronization/list | Available through Atlas; excluded IDs omitted; cursor pagination |
| User search | Unavailable |
| Conversation pagination | Available; account-bound opaque cursors expire after one hour |
| Text history | DM/group text; stable server IDs; no read markers or snap opening |
| Direct text sending | EEL encryption; durable idempotency; confirmed friend snapshot under one hour old |
| Group text sending | Native SDK transport encryption; current membership and exclusions checked |
| Incoming friend requests | Full standalone sync; exclusions and ignored requests filtered; no viewed/accept changes |
| Public profile identity | Username, display name and public flags by recipient UUID; exclusions enforced |
| Own story status | Latest post timestamps and server counters; no media viewing |
| Own story metadata | Adapter implemented, disabled pending live validation |
| Quoted replies, reactions, read state mutations | Unavailable |
| Media upload | PNG/JPEG and H.264/AAC MP4; fresh AES-256-CBC keys; encrypted account storage |
| Snap delivery | Direct EEL and group TLS-only JPEG delivery live-validated; video delivery untested |
| Explicit snap viewing | Unavailable |
| Group creation and membership management | Standalone unavailable |
| My Story publishing | Implemented; packet preparation validated; explicit friends/custom audience, exclusions enforced |
| Remote story listing/viewing/deletion | Unavailable |
| Batch dispatch and remote event ingestion | Unavailable |

The binary also retains legacy browser CLI diagnostics, described in
[LEGACY.md](LEGACY.md). HTTP and MCP do not invoke these commands. Account
metadata defaults to text-only; the service starts no monitors or campaigns.

## MCP setup

Start `snapchat-mcp serve`, then configure your MCP client:

```json
{
  "mcpServers": {
    "snapchat": {
      "command": "/absolute/path/to/snapchat-mcp",
      "args": ["mcp", "--url", "http://127.0.0.1:8787"]
    }
  }
}
```

For a custom service directory, add `--data-dir` to both commands. Alternatively,
use `--token-file` to locate the same private token. MCP reads it locally; do not
put the bearer token in client configuration or command arguments. The stdio
process uses stdout exclusively for MCP protocol messages.

Tool names use `snapchat_{family}_{operation}`, such as
`snapchat_accounts_create`, `snapchat_exclusions_replace`,
`snapchat_jobs_preview`, and `snapchat_messages_send`. Each publishes a JSON
input schema and returns the same structured result as HTTP. Unavailable tools
return `isError: true` and `unsupported_capability`. No arbitrary evaluation,
command execution, auto-reply agent, or credential export is exposed.

## Native runtime diagnostics

The optional native probes require Node 24+ and pinned public Snapchat assets:

```sh
./bin/snapchat-mcp protocol-assets
./bin/snapchat-mcp protocol-check --account "$ACCOUNT_ID" --probe keys
./bin/snapchat-mcp protocol-check --account "$ACCOUNT_ID" --probe session
```

Artifacts are fetched from Snapchat's CDN into `DATA_DIR/runtime`, checked by
SHA-256 before execution, and kept separate from credentials. Their original
runtime license notice is linked from the downloaded JS asset. Credentials enter
the Node subprocess on stdin and are never written to a plaintext temporary file.
Both probes disable network operations and key writes; telemetry requests are
also blocked. `--probe feed` performs a limited read-only synchronization.
The service does not enable sending based on a successful key or session probe.

The HTTP/MCP counterparts are `accounts/verify_keys` and
`accounts/verify_session`. Failure diagnostics contain stages and fixed field or
callback names, rather than native error dumps or key material.

## Text messaging

Call `conversations/list?limit=100` first to cache conversation IDs and versions.
Use `next_cursor` until `has_more` is false; an empty visible page may still have
a next cursor because exclusions apply after paging. Limits are 1–500, default
100. Cursors are opaque, encrypted at rest, scoped to the account, and expire
after one hour. The live check reached the end across multiple pages of visible
conversations with no duplicates. Concurrent Snapchat activity may change paging
order; clients should deduplicate conversation IDs.
`messages/list?conversation_id=UUID&limit=40` returns text events only, with stable
server message IDs. The default limit is 40, maximum 100. `has_more` may be true,
but message cursor pagination is unavailable and returns 501. These reads do not
mark messages read or open snaps.

`POST /v1/accounts/ACCOUNT_ID/messages/send` accepts:

```json
{"conversation_id":"UUID","text":"hello","idempotency_key":"unique-message-key"}
```

Direct sending requires a synchronized conversation and a confirmed friend
snapshot less than one hour old. `friends/list` refreshes that snapshot without
Chromium; bootstrap also captures an initial snapshot. Group sending uses the
same endpoint with a synchronized group conversation ID. The native SDK syncs
membership and checks every current participant against account exclusions before
submission. Group text packets use transport encryption (`encryption: tls_only`),
while direct packets use EEL. Nonempty `reply_to` returns 501. A repeated
key with the same payload returns the stored successful receipt; changed payloads
conflict. An uncertain result blocks resubmission under that key. Do not create a
new key to retry an uncertain delivery without independently checking history.
The service never automatically retries an uncertain send.

## Confirmed friends

`GET /v1/accounts/ACCOUNT_ID/friends/list?limit=500` performs a full read-only Atlas
sync, saves the mutual-friend snapshot in the encrypted account vault, and returns
nonexcluded friends with `id`, `username`, and `display_name`. Pending, following,
blocked, deleted, and unknown relationships never enter the confirmed snapshot.
Use `next_cursor` in the next request to continue (maximum 500 per page). Each
page refreshes from the server, so changes during pagination may affect the list.
`total_confirmed` counts the complete mutual-friend snapshot before exclusions.
The sync never adds or accepts friends. If authentication expires, call
`accounts/refresh`; interactive challenges still require browser login.

## Packet preparation

`POST messages/prepare` accepts `conversation_id` and `text`. It runs native
synchronization, membership checks, encoding, and packet validation, then stops
before the submission HTTP request. `submitted` must be false. Group text
preparation was validated; no live group message was sent during this work.

CLI equivalents:

```sh
./bin/snapchat-mcp protocol-check --account "$ACCOUNT_ID" --probe feed --limit 100
./bin/snapchat-mcp protocol-prepare-send --account "$ACCOUNT_ID" \
  --file CONVERSATION_ID --text "packet check"
```

For CLI pagination, pass the prior response's `next_cursor` through `--cursor`.
The `--probe native-feed` diagnostic retains the older limited native feed check;
normal `conversations/list` uses direct paginated metadata requests.

## Media uploads

`POST media/upload` accepts `content_base64` and `mime_type`. Supported media:

- PNG and JPEG images, at most 8,192 pixels per side and 40 million pixels total.
- MP4 with one H.264 video stream, optional AAC audio, and duration at most 60
  seconds. `ffprobe` must be installed for metadata validation.
- Maximum decoded size: 16 MiB.

Example body:

```json
{"content_base64":"BASE64_FILE_BYTES","mime_type":"image/jpeg"}
```

The service creates fresh AES-256-CBC key/IV material, uploads ciphertext through
Snapchat's signed storage endpoint, and stores the resulting handle and keys in
an encrypted, account-bound record. Storage receives no Snapchat cookies or
bearer token, and redirects are disabled. The response includes `media_id`,
dimensions, SHA-256, and a conservative local expiry of 24 hours; it never returns
keys or a signed upload URL. PNG, JPEG, and MP4 uploads were validated live.
Uploading does not send a snap or publish a story.

`snaps/prepare` accepts `conversation_id` and `media_id`; the CLI adds
`--media-id MEDIA_ID` to `protocol-prepare-send`. Direct EEL and group TLS-only
snap preparation now pass without submitting content. The media upload delegate
and native callback identity collision have been repaired. One authorized direct
JPEG snap received a matching successful RPC acknowledgment with a server message ID.
An authorized group JPEG snap also received a matching successful RPC acknowledgment
using TLS-only transport, with exclusions enforced. Video snap
delivery has not been live-tested.

`snaps/send` is enabled in HTTP and MCP as `snapchat_snaps_send`. It accepts
`conversation_id`, `media_id`, and a stable `idempotency_key`. It enforces exclusions,
checks membership, validates the outgoing packet, and requires a matching server
acknowledgment. Reusing a completed key returns the saved receipt without sending
again; uncertain attempts cannot be automatically retried. There is no Chromium
fallback.

```sh
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"conversation_id":"CONVERSATION_ID","media_id":"MEDIA_ID","idempotency_key":"one-snap-001"}' \
  "$API/v1/accounts/$ACCOUNT_ID/snaps/send"
```

## My Story

Story publishing remains part of both public interfaces even when an account's
audience check blocks publication:

- HTTP: `POST /v1/accounts/{account_id}/stories/publish`
- MCP: `snapchat_stories_publish` with `account_id`, `media_id`, `audience`, and
  `idempotency_key`.

`available: true` means the operation is implemented; individual requests still
require a valid session, uploaded media, and a verified audience. An empty, stale, unverified, or excluded
audience returns HTTP 409 / MCP `isError` with `audience_not_verified`. It does
not disable or remove the publishing interface. Packet preparation and receipt
fixtures have been validated. One friends-audience JPEG publication received a
matching successful Snapchat RPC acknowledgment using an explicit per-request
exclusion override; stored exclusions were unchanged.

`POST stories/prepare` accepts:

```json
{"media_id":"UPLOADED_MEDIA_ID","audience":"custom"}
```

`audience` must be `friends` or `custom`. Custom means the saved Snapchat custom
My Story audience; this API does not change those privacy settings. Both modes
use explicit native privacy metadata and never inherit a public audience.
Preparation returns `audience_check` with size, freshness, `can_publish`, and
`exclusions_enforced: true` by default. It also returns the excluded count,
validated packet metadata, and `submitted: false`.
Image/video story preparation checks the exact remote media reference and exact
audience metadata before any submission request.

`POST stories/publish` adds an `idempotency_key` to that body. Before publishing,
the service performs a fresh full friend sync and evaluates the chosen audience,
including Snapchat's per-friend custom-story permissions. An empty, stale, or
unverified audience, or any excluded viewer, returns `audience_not_verified`
before creating a delivery attempt. To explicitly bypass local exclusions for
one test, pass `"ignore_exclusions": true` to both preparation and publishing.
The chosen Snapchat audience still applies; empty, stale, or unverified audiences
remain blocked. This request-scoped override does not change stored exclusions
or affect direct messages/snaps. Reuse the same override when replaying an
idempotency key. No privacy settings are changed automatically.

Publishing uses the same durable delivery ledger as texts. Story acknowledgment
parsing has fixture tests; image and MP4 packets were validated without posting.
One friends-audience JPEG story was published with a matching server
acknowledgment on 2026-10-19. Video publication and remote playback were not
validated. Remote story listing,
viewing, and deletion are still unavailable.

## Story publication history and deletion

`GET /v1/accounts/{account_id}/stories/published` and MCP
`snapchat_stories_published` return account-scoped, acknowledged publications
recorded by this service. Optional `cursor` and `limit` paginate the local
history. Each item includes `story_id`, `idempotency_key`, `created_at`, receipt
`verification`, and `remote_status: "unknown"`. The response explicitly sets
`source: "local_publication_receipts"` and `remote_state_verified: false`.
It is not a remote feed or confirmation that a story is still visible.

Keep the `story_id` returned by `stories/publish` in your script. It can also be
recovered through `stories/published` after restarting the service.

The reserved deletion contract is `POST /v1/accounts/{account_id}/stories/delete`
with `{"content_id":"STORY_ID_FROM_PUBLICATION"}`, or MCP
`snapchat_stories_delete` with the same field plus `account_id`. **Deletion is
unavailable**: calls return HTTP 501 / MCP `isError`, never a success receipt or
a local-only deletion. A working standalone deletion backend has not been
validated. The signed-in Public Profile manager did not expose the friends-only
test story. [Snapchat's web deletion guidance](https://help.snapchat.com/hc/en-gb/articles/7012309587476-How-to-Save-or-Delete-My-Snapchat-Post-from-the-Web)
says web deletion is not available for Public Profiles. Delete the test story
in the mobile app until this backend is implemented.

## Additional standalone reads

All operations below share the same account-scoped bearer authentication and
schemas between HTTP and MCP:

| HTTP route | MCP tool | Arguments beyond `account_id` |
| --- | --- | --- |
| `GET /v1/accounts/{account_id}/requests/list` | `snapchat_requests_list` | Optional `cursor`, `limit` (1–500) |
| `GET /v1/accounts/{account_id}/profiles/get` | `snapchat_profiles_get` | `recipient_id` (UUID) |
| `GET /v1/accounts/{account_id}/stories/status` | `snapchat_stories_status` | None |

`requests/list` performs a full incoming sync and sorts by UUID. Its cursor is the
last visible UUID; each page is freshly synchronized, so this is not a frozen
snapshot. Ignored, excluded, and self entries are omitted. Responses explicitly
report `marked_viewed: false` and `friendships_changed: false`. This operation does
not accept requests or change the confirmed-friend send eligibility snapshot.

`profiles/get` reads public identity fields only: stable username, mutable
username, display name, and official/popular/chatbot flags. It checks exclusions
before loading credentials or making a request, and rejects mismatched or
multiple returned identities. It does not return private profile information.

`stories/status` queries this account only. It returns
`latest_post_timestamps_ms` and any `active_story_counts` supplied by Snapchat,
with `checked_at`. A latest-post timestamp was validated live; counter decoding
has fixture tests. Timestamps and empty counters do not prove that a particular
story is visible, expired, or deleted. No media is downloaded or marked viewed.

`GET stories/get?content_id=STORY_ID` / `snapchat_stories_get` is reserved for an
owned publication's metadata. Its adapter restricts access to acknowledged
publications in the same account's ledger and discards media/key fields.
**It remains disabled with `unsupported_capability` pending live validation.**
The session expired before this check, and standalone refresh requires an
interactive login. Missing metadata will be treated as unknown rather than
proof of deletion when the adapter is enabled.
