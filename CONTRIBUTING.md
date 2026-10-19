# Development

Use Go 1.26+, Node 24+, and `make`. Run `make check` before a change and regenerate
`docs/openapi.json` with `make spec` after changing the operation catalog or schemas.

## Source layout

- Root Go files: command dispatch, local service setup, Chromium bootstrap, and
  legacy browser diagnostics.
- `bridge.js`: embedded browser adapter for bootstrap and legacy commands.
- `internal/service`: HTTP/MCP registry, storage, vault, protocol adapters, and tests.
- `docs`: API reference, validation evidence, architecture, and publication notes.

Add operations through `Operations()` and dispatch in `core.go`. HTTP, MCP, and
OpenAPI share that definition. Keep unvalidated operations unavailable with a
clear reason; a reserved tool name is not evidence that an action works.

Cover response validation, ownership, exclusions, and redaction with synthetic
fixtures. Sending must validate its destination and packet, persist its attempt,
and match the server acknowledgment. Never retry uncertain delivery automatically.

Do not commit captures, contacts, chat content, credentials, private keys, signed
URLs, or native diagnostic dumps. Keep live state and downloaded runtime assets
outside the repository. Ordinary tests need no Snapchat account. Opt-in offline
native and authenticated preparation checks are in [STANDALONE.md](docs/STANDALONE.md).
Live sends require an explicitly selected audience and stable idempotency key;
they do not belong in default CI.

The native worker, runtime JS, and WASM are pinned together. Update URLs/hashes
after inspecting their interfaces. Each delegate needs a distinct object: Djinni
stores native proxy identity on it. Sharing a Proxy target across interfaces
caused the original snap preparation memory fault.

For runtime changes, check packet/media contracts, offline native behavior,
authenticated preparation, and an explicitly authorized live test of the changed
sending path. Record which destinations and media types were verified.
