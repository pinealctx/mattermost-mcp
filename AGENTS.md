# Repository guidance

- Build a generic CLI and stdio bridge for the official Mattermost MCP endpoint.
- Keep the public repository independent of deployments, private resources, credential stores and agent skill frameworks. Examples use `chat.example.com` and synthetic data.
- Use Go, Cobra and the official MCP Go SDK. Keep authentication and the read-only message-sync REST adapter in `internal/mattermost`; MCP tools and writes go through the official endpoint.
- Keep CLI flag/environment compatibility documented in README. Generate fixed command signatures only from pinned public upstream source. Cache only server-verified account data in the OS user cache directory. Do not persist credentials or log secrets. Treat tool failures as nonzero CLI exit status and preserve them through the bridge.
- Do not implicitly log out injected credentials or retry writes after ambiguous failures.
- Validate with `make test`, `make lint`, and `make build`. Tests must not access live deployments.

Layout: `cmd/` CLI, `internal/config/` configuration, `internal/mattermost/` authentication and upstream MCP, `pkg/server/` local bridge, `internal/testutil/` isolated protocol fixtures.
