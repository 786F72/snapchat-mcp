# Snapchat MCP

Snapchat HTTP API and MCP server.

## Features

| Feature | What you can do |
| --- | --- |
| Friends | List and search friends, check incoming requests, look up public profiles |
| Chats | List conversations and groups, read older messages, search message text |
| Messages | Send texts and JPEG snaps to friends or groups |
| Inbox | Check for incoming texts and snap notifications without opening snaps |
| Stories | Publish to My Story for friends or your saved custom audience |
| Uploads | Upload PNG, JPEG, and MP4 files for use in snaps and stories |
| Delivery history | Check whether Snapchat accepted a send and look up previous attempts |

## Requirements

- Go 1.26+
- Node.js 24+
- Chrome or Chromium for sign-in
- `ffprobe` for MP4 uploads

## Setup

### 1. Build

```sh
git clone https://github.com/786F72/snapchat-mcp.git
cd snapchat-mcp
```

**Linux / macOS**

```sh
go build -trimpath -o bin/snapchat-mcp .
```

**Windows**

```powershell
go build -trimpath -o bin/snapchat-mcp.exe .
```

### 2. Sign in

Open Snapchat in a separate browser profile:

<details>
<summary>Linux</summary>

```sh
chromium --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 --user-data-dir="$HOME/.config/snapchat-mcp/chrome" https://web.snapchat.com/
```

</details>

<details>
<summary>macOS</summary>

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 --user-data-dir="$HOME/Library/Application Support/snapchat-mcp/chrome" https://web.snapchat.com/
```

</details>

<details>
<summary>Windows</summary>

```powershell
& "$env:ProgramFiles\Google\Chrome\Application\chrome.exe" --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 "--user-data-dir=$env:LOCALAPPDATA\snapchat-mcp\chrome" https://web.snapchat.com/
```

</details>

Sign in, wait for chats to load, and keep one Snapchat tab open. In another
terminal, save the session and start the server:

**Linux / macOS**

```sh
./bin/snapchat-mcp connect
./bin/snapchat-mcp serve
```

**Windows**

```powershell
.\bin\snapchat-mcp.exe connect
.\bin\snapchat-mcp.exe serve
```

`connect` saves your login, downloads the Snapchat messaging files it needs, and
checks the connection. You can then close Chrome. Leave `serve` running.
The HTTP API listens at `http://127.0.0.1:8787`.

### 3. Add it to your MCP client

```json
{
  "mcpServers": {
    "snapchat": {
      "command": "/absolute/path/to/snapchat-mcp/bin/snapchat-mcp",
      "args": ["mcp"]
    }
  }
}
```

Set `command` to the full path of the executable. The MCP process connects to
`serve` automatically. Call `snapchat_accounts_list` to get your `account_id`;
pass it to tools that operate on your Snapchat account.

## Useful tools

| Task | MCP tool |
| --- | --- |
| Check your connection | `snapchat_accounts_verify_session` |
| Find a friend | `snapchat_friends_search` or `snapchat_recipients_resolve` |
| Refresh the conversation list | `snapchat_conversations_list` |
| Find a saved conversation | `snapchat_conversations_search` |
| Read messages | `snapchat_messages_list` |
| Search messages | `snapchat_messages_search` |
| Check for new messages | `snapchat_inbox_poll` |
| Send a text | `snapchat_messages_send` |
| Upload a file | `snapchat_media_upload_file` |
| Send an uploaded snap | `snapchat_snaps_send` |
| Publish an uploaded story | `snapchat_stories_publish` |
| Check a send | `snapchat_deliveries_get` or `snapchat_deliveries_list` |

Search messages one conversation at a time. Pass `next_cursor` to continue into
older messages, even when a page has no matches. Friend and conversation searches
use saved lists; call `snapchat_friends_list` or `snapchat_conversations_list`
to refresh them.

Inbox polling returns text and snap details, not snap images. Save and reuse its
`next_cursor` after every call. See [inbox polling](docs/INBOX.md) for the response
fields and limits.

For file uploads, start the server with a directory it can read:

```sh
./bin/snapchat-mcp serve --media-root /path/to/media
```

Upload first, then pass the returned `media_id` to the snap or story tool. Each
send also requires an `idempotency_key`: use the same key when checking or retrying
the same send so you don't accidentally send it twice.

## Connection commands

| Command | Purpose |
| --- | --- |
| `connect` | Save a signed-in browser session, or reconnect after signing in again |
| `status` | Show saved session details |
| `status --verify` | Check whether Snapchat accepts the session |
| `refresh` | Renew the session without opening Chrome |
| `doctor` | Check setup requirements and saved session files |
| `version` | Print the version |

Use `--account ACCOUNT_ID` when you have multiple accounts. `--label NAME` selects
an account by its saved label; `connect` creates it if it doesn't exist.

## Limits

- Adding or accepting friends, creating or editing groups, reactions, quoted
  replies, marking chats read, and opening snaps are not supported yet.
- Story viewing, remote story feeds, and story deletion are not supported.
- JPEG snap sending has been tested in direct and group chats. Video snap and
  video story sending have not been tested against Snapchat.
- Group text packets have been checked, but live group text delivery has not
  been tested.
- Message history only includes messages Snapchat still returns. It cannot
  recover expired or deleted messages.
- Inbox polling checks recent activity; it isn't a push notification service.

See [tested features and limitations](docs/STANDALONE.md) for details.

## Saved data

Sessions, settings, and the database are stored in `qq_snap` under your system's
configuration directory. Use `--data-dir /path/to/data` consistently if you choose
another location. Keep that folder private and outside the repository.
See [security](SECURITY.md) for storage details.

## Documentation

- [HTTP API reference](docs/API.md)
- [Inbox polling](docs/INBOX.md)
- [Tested features and limitations](docs/STANDALONE.md)
- [How it works](docs/ARCHITECTURE.md)
- [Development](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md)
- [Third-party components](THIRD_PARTY.md)

## Development

```sh
make check
make build
make spec
```

The default tests use sample data and don't sign in or send messages.
