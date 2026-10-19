# Third-party components

This unofficial Snapchat integration is not endorsed by Snap Inc. Snapchat names
and runtime assets belong to their respective owners.

Snapchat worker JavaScript, runtime JavaScript, and messaging WASM are downloaded
from pinned public CDN URLs by `protocol-assets`. These assets are not included
in the repository. Hashes are checked before loading.

Go dependencies are pinned in `go.mod` and `go.sum`, including:

- `github.com/0xzer/snapper`: Snapchat protobuf definitions.
- `github.com/gobwas/ws`: Chromium DevTools WebSocket transport.
- `github.com/modelcontextprotocol/go-sdk`: MCP server and schemas.
- `github.com/zalando/go-keyring`: OS keyring integration.
- `google.golang.org/protobuf`: protobuf runtime.
- `modernc.org/sqlite`: local SQLite persistence.

Dependency license terms remain with their respective projects, including
transitive dependencies. Consult module sources when distributing binaries.
No open source license has been selected for this project's own code.
