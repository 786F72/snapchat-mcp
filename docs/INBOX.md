# Inbox polling

`inbox.poll` (HTTP) / `snapchat_inbox_poll` (MCP) returns incoming text and snap
metadata without opening snaps or changing Snapchat read state. It is a pull
operation, not a background auto-reply agent or push subscription.

## Request

```sh
curl -sS -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"limit":40,"max_conversations":3}' \
  "$API/v1/accounts/$ACCOUNT_ID/inbox/poll"
```

For MCP, provide `account_id` with the same arguments. On subsequent calls,
include the previous `data.next_cursor`. Always save the next cursor after
processing its page, even if the page is empty.

| Field | Meaning |
| --- | --- |
| `cursor` | Optional opaque account-bound checkpoint; omit for initial backlog |
| `limit` | At most 1–100 persisted event records scanned, default 40 |
| `max_conversations` | At most 1–5 changed/uninitialized histories fetched, default 3 |

## Response

```json
{
  "data": {
    "items": [
      {
        "event_id": "CONVERSATION_UUID/42",
        "conversation_id": "CONVERSATION_UUID",
        "message_id": "42",
        "sender_id": "RECIPIENT_UUID",
        "type": "text",
        "text": "Example incoming text",
        "created_at": 1700000000000
      }
    ],
    "next_cursor": "OPAQUE_ACCOUNT_BOUND_CHECKPOINT",
    "has_more": false,
    "initial_sync": true,
    "sync": {
      "metadata_synced": true,
      "conversations_fetched": 1,
      "pending_conversations": 0,
      "more_work": false,
      "history_gap_possible": [],
      "errors": [],
      "history_window_limit": 100
    },
    "media_opened": false,
    "read_receipts_sent": false
  }
}
```

A snap event has `type: "snap"`, identifiers, sender, and timestamp, but no `text`,
media URL, encryption key, image content, or download reference. Outgoing
messages and nontext chat status events are not returned. Incoming group history
may include former members, subject to local exclusions.

## Cursor behavior

- Cursors are encrypted/authenticated and bound to the local account. They survive
  process restarts; account key changes or replacing the database can invalidate
  them. They do not depend on the one-hour conversation pagination cursor.
- Replaying a cursor replays available events after that position. `event_id` is
  stable within the account; clients should deduplicate by account and event ID.
- Events follow durable ingestion order, not global message timestamp order.
  `created_at` preserves the native message timestamp (milliseconds).
- `has_more` describes the saved event backlog. `sync.more_work` separately reports
  pending history reads, metadata pages, or synchronization errors.
- A page may be empty after exclusions filter its scanned records. Advance using
  `next_cursor` and continue while either more-work flag is true.
- With no cursor, events begin at the start of the account's retained inbox log,
  including recent history captured during initial synchronization.

## Synchronization and limits

Each poll checks the first conversation metadata page and continues one older
page when a scan is in progress. History is fetched only for uninitialized or
changed cached conversations, based on version and latest activity timestamp.
When an activity timestamp is missing, history becomes eligible for a fallback
check after one minute. Historical reads are bounded by `max_conversations` and
run concurrently, with a 60-second synchronization deadline.

The existing native fetch retrieves a recent window of up to 100 events per
conversation. Inbox polling does not paginate older history automatically; use
`messages.list` or `messages.search` separately for that, starting in 0.6.0.
`history_gap_possible` lists conversations whose window may omit earlier events
or no longer overlaps the previous checkpoint. Gap flags persist across lost
responses/restarts and are not silently cleared. Events that expire before a
poll, or fall outside that window, cannot be guaranteed captured.

When remote sync fails, already saved events can still be returned with
`sync.errors` containing sanitized codes. A successful HTTP response with
`metadata_synced: false` or synchronization errors is not evidence that the
remote inbox is empty. Refresh or recapture credentials on `reauth_required`.

Exclusions apply during synchronization and again when returning saved events.
A currently excluded group member blocks the group's events; excluded senders
are filtered as well. Excluded events consume scan positions without being
returned. Polling never sends content or invokes snap viewing/read-state APIs.

## Storage

Event payloads are encrypted in the private service database. Unique account,
conversation, and server message IDs suppress duplicates. Persisting a window
and its processed metadata checkpoint is one transaction; failures remain
pending for later polls. Metadata scan position is also durable. Inbox records
are separate from `events.list`, which still describes local service activity.
No automatic inbox retention/pruning policy is implemented in this release.

## Incoming event stream

`GET /v1/accounts/{account_id}/inbox/stream` returns server-sent events (SSE),
using the same bearer token as other HTTP endpoints. Each connection polls the
bounded inbox, waits ten seconds after a page, then polls again. This lets HTTP
clients keep one connection open instead of making repeated polling requests.
It does not use Snapchat push notifications or webhooks. MCP clients continue
to use `snapchat_inbox_poll`.

Each `inbox.page` event contains a complete `InboxPage` JSON object and an `id`
equal to its opaque `next_cursor`. Process the entire page before saving its
checkpoint. Reconnect with `Last-Event-ID: CHECKPOINT` or `?cursor=CHECKPOINT`.
The header takes precedence. A partial network frame must not advance your
checkpoint; stable event IDs let you deduplicate a replay.

Empty pages are emitted too. Check `sync.errors` and gap indicators even when
there are no messages. A top-level polling failure after streaming starts emits
`inbox.error` with a sanitized code and closes the stream. Invalid initial
checkpoints return an ordinary HTTP error. Snap media stays unopened, and no
read receipts are sent.
