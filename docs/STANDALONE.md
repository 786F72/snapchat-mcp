# Protocol validation and limits

Last validation: **2026-10-19**. Live checks used a private account; recipient
identifiers, conversations, counts, and receipts are excluded here. This records
observed behavior, not a guarantee for every account or runtime version.

## Evidence

| Operation | Verification |
| --- | --- |
| Session refresh | Standalone refresh after restart with Chromium closed |
| Identity restore | Captured version-10 identity restored offline without registration |
| Friends | Full Atlas sync, confirmed mutual friends only, exclusions applied |
| Conversations | Multiple pages reached end of feed without duplicate entries |
| Text history | Direct/group text matched reference history; stable server IDs |
| History pagination/search | Three-page direct/group reads and search continuation; no media opens/read receipts |
| Direct text | Authorized EEL send independently verified in server history |
| Group text | Packet preparation validated; live delivery not tested |
| Media upload | PNG, JPEG, H.264 MP4 uploaded with encrypted account-bound handles |
| Direct snap | Authorized JPEG send acknowledged by matching RPC response |
| Group snap | Authorized JPEG send through HTTP acknowledged by matching RPC response |
| My Story | Authorized friends-audience JPEG publication acknowledged with story ID |
| Requests | Read-only sync; ignored/excluded requests filtered |
| Public profile | Own public identity matched the requested UUID |
| Story status | Post timestamps and counters returned without viewing media |
| Receipt replay | Completed key returned its receipt without another submission |

Direct snaps used EEL. The tested group used a TLS-only message envelope; uploaded
media remains AES-encrypted. Both sends enforced exclusions and matched their
intended destination and client resolution ID in the acknowledgment. No incoming
snaps were opened for these checks.

The story test used an explicitly authorized request-scoped exclusion override.
Stored exclusions were unchanged; default preparation still blocked excluded
viewers in that audience.

## Native snap repair

Conversation snaps originally crashed before submission. Two adapter issues were
repaired: a missing media upload delegate and conversation/feed delegates sharing
a Proxy target. Djinni caches native interface identity on its JS object; sharing
a target could dispatch through the wrong interface.

Delegates now have separate explicit objects. The uploader resolves one uploaded
handle, returns the native upload result, and attaches AES metadata at completion.
Offline checks validate distinct identities and a packet with all network access
disabled. Authenticated direct/group preparation passed before live JPEG tests.

## Remaining limits

- Video snap delivery and video story publishing have not been live-tested.
- Group text delivery has preparation evidence only.
- History pagination covers retained text, skipping media content. It cannot
  recover messages Snapchat no longer returns. Inbox polling still scans bounded
  recent windows rather than automatically walking older history.
- Own story metadata is implemented but disabled pending live validation.
- Story deletion is unimplemented and unavailable. A missing record or zero
  counter does not establish deletion. The test story was not deleted through
  this service.
- Remote story feeds/viewing, snap opening, accepting requests, group management,
  remote user search, reactions, quoted replies, native push and webhooks are
  unavailable. Text jobs and polling-backed incoming SSE are available.
- Events describe local service changes, not Snapchat notifications.
- `protocol_ready: false` denotes incomplete overall coverage; consult individual
  capabilities for implemented operations.

Unavailable operations return HTTP 501 / MCP `isError` with
`unsupported_capability`. There is no browser fallback.

## Checks

Version 0.6.0 adds history pagination and page-scoped search. Native continuation
requires SDK-local handles, not stable server IDs; using server IDs repeats the
first page. Handles (including zero) stay inside encrypted cursors with a server
boundary for progress validation. Fixtures cover account/conversation/filter
isolation, expiry, restart recovery, media-only pages, and mid-fetch exclusions.

Opt-in `TestLiveHistoryPagination`, with `QQ_SNAP_LIVE_ACCOUNT` and
`QQ_SNAP_LIVE_DATA_DIR` set, validated three pages of direct and group history,
search continuation, and direct-text preparation without submission. It logs
aggregate results only. The launcher regression test executes over 40 KiB of
source through stdin and verifies source/credentials are absent from argv.

Connection and local discovery tools added in 0.4.0/0.5.0 use fixture coverage
for account selection, incomplete/locked sessions, credential redaction,
account renaming, cache exclusions, delivery filters, and pagination. Windows
checks cover exclusive journal locking and owner-only token ACL rejection.
Local diagnostics never claim that stored credentials are currently accepted.

`make check` uses synthetic fixtures without live credentials.

The opt-in offline native test needs an encrypted local account and valid uploaded
handle, but no valid auth token or network access:

```sh
QQ_SNAP_OFFLINE_DATA_DIR="$HOME/.config/qq_snap" \
QQ_SNAP_OFFLINE_ACCOUNT=ACCOUNT_ID \
QQ_SNAP_OFFLINE_MEDIA_ID=MEDIA_ID \
go test ./internal/service -run TestOfflinePreuploadedMediaNativeContract -v
```

Authenticated preparation stops before submitting `CreateContentMessage`:

```sh
QQ_SNAP_LIVE_DATA_DIR="$HOME/.config/qq_snap" \
QQ_SNAP_LIVE_ACCOUNT=ACCOUNT_ID \
QQ_SNAP_LIVE_MEDIA_ID=MEDIA_ID \
QQ_SNAP_LIVE_DIRECT_CONVERSATION=DIRECT_CONVERSATION_ID \
QQ_SNAP_LIVE_GROUP_CONVERSATION=GROUP_CONVERSATION_ID \
go test ./internal/service -run TestLiveSnapPreparation -v
```

Read adapter validation uses `TestLiveReadActions` with the live account/data
variables. An optional owned story ID exercises its disabled adapter directly.
These opt-in checks are excluded from CI.

## Agent helper validation (0.2.0)

Friend search/resolution, cached conversation/group metadata, individual exclusion
edits, and delivery status lookup are local operations covered by fixture tests
and HTTP/MCP parity checks. Tests cover account isolation, stale data, ambiguous
matches, atomic exclusion changes, and receipt redaction.

File uploads use the already validated encrypted upload pipeline after a confined
local read. Filesystem tests cover disabled roots, outside paths, escaping
symlinks, directories, empty/oversized files, and media validation before auth.
No new live sends or uploads were performed for these workflow helpers.

A read-only smoke check against the captured local account also passed friend
search, recipient resolution, cached group lookup, and saved delivery lookup.
Legacy bootstrap snapshots use their capture timestamp when the separate friend
sync timestamp is absent; new captures record both timestamps.

## Inbox polling validation (0.3.0)

Fixture tests cover encrypted storage, account-bound and tamper-resistant cursors,
restart recovery, stable event IDs, duplicate suppression, outgoing filtering,
post-ingestion exclusions, metadata continuation, bounded work, partial failure
recovery, and persistent history-gap indicators. Activity timestamps trigger reads
when the conversation version is unchanged. Missing timestamps get a periodic
fallback check. HTTP and MCP return equivalent events/checkpoints.

The native event projector is tested separately: text is decoded, snap payloads
are not decoded, internal media references are never returned, and unstable
identifiers are rejected. The native runtime network allowlist does not permit
snap opening, viewing, or read-state mutations for inbox calls.

A bounded live read synchronized metadata and multiple conversation windows,
returned incoming text events, and replayed their stable IDs from an older
checkpoint without reinserting duplicates. Snap metadata projection has fixture
coverage; no live incoming snap was opened for validation. No messages were sent
and no read receipts were submitted. Expired stored credentials correctly
reported `reauth_required`; capturing the existing signed-in browser session
restored polling without changing device keys.

## Scheduling, streams, and account disconnect (0.7.0)

A persisted text job was claimed by the scheduler and delivered to one explicitly
selected direct conversation. Snapchat returned a matching successful message
acknowledgment. A stable job key prevents the live test from sending again.
Scheduled group texts reuse the existing group send path; live group text delivery
still has no new evidence.

Fixtures cover encrypted job content, duplicate creation, account isolation,
restart execution, future times, cancellation, changed exclusions, competing
workers, uncertain outcomes, and scheduler shutdown. Incoming SSE fixtures cover
complete-page checkpointing, reconnects, and cross-account cursor rejection.
The SSE endpoint uses the validated inbox poller and does not open snap media.

Service logout fixtures verify session removal, queued-job cancellation, retained
account identity, late-refresh rejection, and reconnection by the same user.
No live logout was performed on the working account.

Rename and leave requests passed authenticated preparation without submission.
A live rename/restore check received matching responses without a successful
update flag; a subsequent read confirmed the saved original title. Actual group
changes remain disabled. Packet fixtures reject changed actors, destinations,
titles, leave targets, versions, additional actions, and malformed frames.

Opt-in preparation uses `TestLiveGroupActionPreparation` with
`QQ_SNAP_LIVE_ACCOUNT`, `QQ_SNAP_LIVE_DATA_DIR`, and an explicitly selected
`QQ_SNAP_LIVE_GROUP_CONVERSATION`. `TestLiveScheduledText` additionally requires
`QQ_SNAP_LIVE_MUTATE=true` and `QQ_SNAP_LIVE_CONVERSATION`. It sends one text using
a fixed job key. These tests skip by default and never run in ordinary CI.
