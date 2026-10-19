# Changelog

## 0.3.0 — 2026-10-19

- Added `inbox.poll` / `snapchat_inbox_poll` for incoming texts and snap metadata.
- Persisted encrypted events and account-bound cursors for restart/reconnects.
- Added bounded metadata scanning, activity/version change detection, duplicate
  suppression, exclusions, partial-failure reporting, and history-gap indicators.
- Polling does not open snaps, download media, mark chats read, or send messages.
- Full message history pagination remains unavailable; this implements inbox
  polling only.

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
