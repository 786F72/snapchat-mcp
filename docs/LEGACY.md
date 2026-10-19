# Legacy browser diagnostics

The executable retains `inspect`, `friends`, `check`, `eval`, and browser `send`
commands from the original CLI. They are separate from HTTP/MCP and need a
signed-in Chromium debugging session. Bootstrap uses the embedded browser adapter
to capture an existing session.

The default legacy command previews recipients. Only explicit `--send` submits
text through that path. Prefer the account-scoped service operations and their
exclusions, documented in [API.md](API.md).

Offline HAR inspection:

```sh
./bin/snapchat-mcp inspect --har /private/path/capture.har
```

HAR files and output reports must remain outside Git. `eval` executes local
JavaScript in the signed-in page and should only use reviewed scripts. It is not
exposed through HTTP or MCP.

Campaign scripts, recurring workers, contact exports, private journals, generated
media, and local account state are excluded from this publication checkout.
