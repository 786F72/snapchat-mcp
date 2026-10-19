# Snapchat MCP

A local Snapchat HTTP API and stdio MCP server. One account-scoped operation
registry drives both interfaces and the generated OpenAPI specification.

Public repository: **786F72/snapchat-mcp**. This unofficial integration uses
pinned Snapchat web runtime assets and implements a limited set of operations.

## Features

| Area | Implemented scope |
| --- | --- |
| Accounts | Account isolation, encrypted credential capture, standalone refresh |
| Contacts | Confirmed friends, incoming requests, public identity lookup, exclusions |
| Conversations | Paginated metadata and text history without read receipts |
| Texts | Direct EEL and group transport-encrypted sending with durable idempotency |
| Media | Encrypted PNG/JPEG and H.264/AAC MP4 upload |
| Snaps | Direct EEL and group TLS-only JPEG delivery acknowledged in live tests |
| Stories | Guarded friends/custom My Story publishing and local publication receipts |
| MCP | Typed tools forwarding to the same authenticated local API |

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

## Documentation

- [HTTP API, MCP, sending, media, and errors](docs/API.md)
- [Validation and limitations](docs/STANDALONE.md)
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
