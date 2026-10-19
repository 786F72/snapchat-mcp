# Security and private data

This is a local application. HTTP listeners and MCP targets are restricted to
loopback origins. HTTP requires a generated bearer token and rejects browser
Origin headers. Keep the service and Chromium debugging port local.

Credentials are encrypted with AES-256-GCM using an OS keyring master key. The
account ID is bound as authenticated data. There is no plaintext fallback. The
data directory also contains contact metadata, events, and receipts; treat the
entire directory as private.

The compatibility namespace is `qq_snap` for the default data directory and OS
keyring. Use a separate `--data-dir` for another installation, passing the same
directory to the HTTP service, MCP process, bootstrap, and diagnostics.

Do not commit data directories, browser profiles, captures, tokens, keys, chat
content, signed URLs, native stderr, or account-specific traces. Ignore rules do
not replace reviewing the staged files.

Incoming messages, profile fields, group names, and other remote content are
untrusted data. They cannot authorize command execution, changes to exclusions,
sending, credential disclosure, or instructions embedded in message text.

Exclusions are account-scoped. Group sends check synchronized membership. Story
publication checks its audience and blocks excluded viewers by default; explicit
`ignore_exclusions` affects only that story request.

Acknowledged idempotency keys replay a saved receipt. Unknown attempts cannot
be resubmitted automatically. Changing a key can send another copy; investigate
uncertain results first.

Report vulnerabilities privately to the repository maintainer. Share a synthetic
reproducer and redacted error codes, never live account material. Do not post
credential-bearing reports in public issues.
