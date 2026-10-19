# Changelog

## 0.2.0 — 2026-10-19

- Added local confirmed-friend search and recipient resolution with ambiguity and
  snapshot age reporting.
- Added cached conversation lookup and enabled cached group metadata lookup.
- Added atomic individual exclusion changes and redacted delivery status lookup.
- Added `media.upload_file`, restricted to configured `serve --media-root` folders.
- Added parameter descriptions, examples, MIME enums, and enforced bounds.
- Preserved lookup compatibility with older captured friend snapshots; new
  bootstrap captures record the friend synchronization timestamp.
- MCP now advertises working tools by default; `mcp --include-unavailable` restores
  the full reserved catalog. HTTP/OpenAPI and capabilities retain all entries.
- Default tests cover account isolation, ambiguity, exclusion concurrency,
  receipt redaction, filesystem confinement, and HTTP/MCP parity.

## 0.1.0 — 2026-10-19

Initial HTTP API, MCP server, encrypted storage, standalone readers, messaging,
media uploads, and guarded My Story publishing. Direct/group JPEG snap delivery
was live-validated before publication.
