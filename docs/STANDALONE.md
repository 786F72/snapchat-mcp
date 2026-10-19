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
- Message history pagination is incomplete; `has_more` does not imply supported
  pagination. Conversation metadata pagination is implemented.
- Own story metadata is implemented but disabled pending live validation.
- Story deletion is unimplemented and unavailable. A missing record or zero
  counter does not establish deletion. The test story was not deleted through
  this service.
- Remote story feeds/viewing, snap opening, accepting requests, group management,
  search, reactions, quoted replies, batch jobs, and remote event ingestion are
  unavailable in the standalone API.
- Events describe local service changes, not Snapchat notifications.
- `protocol_ready: false` denotes incomplete overall coverage; consult individual
  capabilities for implemented operations.

Unavailable operations return HTTP 501 / MCP `isError` with
`unsupported_capability`. There is no browser fallback.

## Checks

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
