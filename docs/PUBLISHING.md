# Repository publication

Target: **786F72/snapchat-mcp**, public visibility, default branch `main`.
The repo contains source and synthetic fixtures. It does not contain a signed-in
account, captured conversations, or downloaded Snapchat runtime assets.

Before pushing:

```sh
make check
make build
make spec
git diff --check
git status --short
git diff --cached --stat
```

Review staged paths and content. Runtime downloads, binaries, credentials,
captures, media, chat history, live account IDs, and contact exports must not
enter Git history. Ignore rules are a guard, not a substitute for review.

Push with credentials for `786F72` and verify the result:

```sh
gh repo view 786F72/snapchat-mcp --json nameWithOwner,isPrivate,url
```

CI runs Linux fixture tests, Go race checks, vet, the build, and an OpenAPI drift
check, plus a Windows fixture/vet/build/JavaScript job. Windows symlink tests skip
only when the runner lacks the required privilege.
It receives no Snapchat credentials and never runs live preparation or sending.
No release binaries or Snapchat runtime assets are published by CI.
