# Snapchat MCP

A local Snapchat HTTP API and stdio MCP server. One account-scoped operation
registry drives both interfaces and the generated OpenAPI specification.

Public repository: **786F72/snapchat-mcp**. This unofficial integration uses
pinned Snapchat web runtime assets and implements a limited set of operations.

## Features

| Area | Implemented scope |
| --- | --- |
| Accounts | Account isolation, local renaming, encrypted credential capture, standalone refresh, status and diagnostics |
| Contacts | Confirmed friends, local search/resolution, incoming requests, public identity, exclusions |
| Inbox | Cursor-based polling for incoming texts and snap metadata; no viewing/read receipts |
| Conversations | Paginated metadata, cached search and group listing, text history |
| Texts | Direct EEL and group transport-encrypted sending with durable idempotency |
| Media | Encrypted PNG/JPEG and H.264/AAC MP4 uploads from bytes or allowed local files |
| Snaps | Direct EEL and group TLS-only JPEG delivery acknowledged in live tests |
| Stories | Guarded friends/custom My Story publishing and local publication receipts |
| MCP | Typed tools forwarding to the same authenticated local API |
| Deliveries | Paginated local history, status filters, redacted receipts and retry eligibility |

Story deletion, remote story feeds, group management, accepting requests, and
other catalog entries remain unavailable. Video snap delivery has not been
live-tested. See [validation and limits](docs/STANDALONE.md) for the evidence.

## Requirements

- Go 1.26+ to build, Node.js 24+ for native JS/WASM execution.
- An unlocked OS keyring for credentials (desktop Secret Service on Linux).
- Chromium with a separate debugging profile for initial authentication.
- `ffprobe` for MP4 metadata validation; `curl` for HTTP examples.

## Quick start

```sh
git clone git@github.com:786F72/snapchat-mcp.git
cd snapchat-mcp
make build
./bin/snapchat-mcp protocol-assets
./bin/snapchat-mcp serve --init-keyring
```

The service listens at `http://127.0.0.1:8787`. In another terminal, register a
local account:

```sh
API=http://127.0.0.1:8787
TOKEN=$(cat "${XDG_CONFIG_HOME:-$HOME/.config}/qq_snap/token")
curl -sS -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"label":"personal"}' "$API/v1/accounts"
```

Set `ACCOUNT_ID` to the returned `data.id`. Open a dedicated browser profile,
sign in manually, and wait for chats to load:

```sh
chromium --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --user-data-dir="$HOME/.local/share/snapchat-mcp/chrome" \
  https://web.snapchat.com/
```

Then capture the existing session from a separate terminal:

```sh
./bin/snapchat-mcp bootstrap --account "$ACCOUNT_ID"
```

Bootstrap stores credentials in the encrypted vault without registering new
device keys. Implemented API operations then run under Node without browser
fallback. Refresh or recapture an expired session on `reauth_required`.

For easier setup or reconnecting, keep one signed-in Snapchat tab in the debugging
browser and run:

```sh
./bin/snapchat-mcp connect
./bin/snapchat-mcp status
./bin/snapchat-mcp status --verify
./bin/snapchat-mcp doctor
./bin/snapchat-mcp refresh
./bin/snapchat-mcp version
```

`connect` reuses the only local account or creates `personal` when none exists,
provisions the OS keyring if needed, captures the session, installs missing
runtime assets, and verifies the session. It does not launch a browser or send
messages. Select `--account ACCOUNT_ID` or an exact `--label LABEL` when multiple
accounts exist. Run it again to reconnect after signing in to the debugging
browser. `--verify=false` captures without the subsequent live verification.

`status` checks local storage only; `captured` does not establish current login
validity. `status --verify` checks the session against Snapchat. The local check
is also available as `accounts.session_status` / `snapchat_accounts_session_status`.
Status responses never contain tokens, cookies, or identity keys.

`doctor` checks Node.js, stored session material, runtime files, and cache counts
locally. It exits nonzero when prerequisites are missing; a passing check still
requires live verification. `refresh` renews the captured session without Chrome
and then verifies it; use `--verify=false` to refresh only. Interactive sign-in
may still be required when Snapchat rejects renewal.

On Windows, build with `go build -trimpath -o bin/snapchat-mcp.exe .` and use
`.\bin\snapchat-mcp.exe` in place of `./bin/snapchat-mcp`. Credentials use Windows
Credential Manager; the local API token has an owner-only Windows ACL.

Persistent storage retains the compatibility namespace `qq_snap`: by default
`~/.config/qq_snap` on Linux, and the `qq_snap` OS keyring service. Use `--data-dir`
consistently on `serve`, `mcp`, bootstrap, and diagnostics for another installation.
Keep this directory outside the repository.

## MCP configuration

Start the HTTP service first, then add this entry to your client's MCP config:

```json
{
  "mcpServers": {
    "snapchat": {
      "command": "/absolute/path/to/snapchat-mcp/bin/snapchat-mcp",
      "args": ["mcp", "--url", "http://127.0.0.1:8787"]
    }
  }
}
```

MCP reads the local token file; do not put its value in client configuration.
Account-scoped tools require `account_id`. Names include `snapchat_friends_list`,
`snapchat_messages_send`, and `snapchat_snaps_send`.

## Poll the inbox

Call `snapchat_inbox_poll` with `account_id` (optional `limit`, default 40;
`max_conversations`, default 3). Pass the returned `next_cursor` on subsequent
calls, including after empty pages. Cursors survive service restarts. Replaying
a cursor can replay events; use stable `event_id` values to deduplicate.

The first poll may include recent backlog. Check `sync.errors`,
`sync.more_work`, and `sync.history_gap_possible` before interpreting an empty
page as no activity. Polling fetches bounded recent windows; full message history
pagination remains unavailable. Snap events contain metadata only and polling
never opens media or marks chats read. See [inbox details](docs/INBOX.md).

## Tools for common tasks

- `snapchat_friends_search` / `snapchat_recipients_resolve`: find usernames or names
  in the last synchronized friend snapshot, with local exclusions applied. Partial
  or duplicate names return candidates; the model must select an exact identity.
- `snapchat_conversations_get` / `snapchat_groups_get`: inspect cached metadata,
  including participants and snapshot age.
- `snapchat_exclusions_add` / `snapchat_exclusions_remove`: edit one UUID atomically.
- `snapchat_deliveries_get`: inspect one attempt by its stable idempotency key.
- `snapchat_deliveries_list`: paginate saved attempts, optionally filtering
  `state` to `pending`, `unknown`, `not_sent`, or `sent`. It never retries sends.
- `snapchat_conversations_search` / `snapchat_groups_list`: find cached titles,
  conversation IDs, or participant UUIDs. Optional `participant_id` is an exact
  filter; synchronize `conversations.list` to refresh stale cache data.
- `snapchat_accounts_rename`: change a local label without changing the Snapchat
  profile or captured credentials.
- `snapchat_accounts_diagnostics`: inspect local connection prerequisites and
  account-scoped cache counts.
- `snapchat_media_upload_file`: upload a file without passing base64 to the model.

Local file upload requires an explicit directory on the server host:

```sh
mkdir -p "$HOME/SnapchatMedia"
./bin/snapchat-mcp serve --media-root "$HOME/SnapchatMedia"
```

Roots can be repeated. Only regular files within those roots are accepted; links
escaping the roots are blocked. Upload does not send a snap. Use the returned
`media_id` with `snapchat_snaps_send` or `snapchat_stories_publish`.

MCP advertises working tools by default. Start it with `--include-unavailable`
to inspect unfinished tools. `capabilities.list` and OpenAPI always include the
full catalog. See [the changelog](CHANGELOG.md) for interface changes.

## Documentation

- [HTTP API, MCP, sending, media, and errors](docs/API.md)
- [Validation and limitations](docs/STANDALONE.md)
- [Inbox polling](docs/INBOX.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Legacy browser diagnostics](docs/LEGACY.md)
- [Publication](docs/PUBLISHING.md)
- [Development](CONTRIBUTING.md), [security](SECURITY.md), [third-party notices](THIRD_PARTY.md)

## Development

```sh
make check
make build
make spec
```

Default checks use synthetic fixtures and do not log in or send content. Live
preparation tests are opt-in. This checkout contains no campaign workers or
auto-reply agents.
