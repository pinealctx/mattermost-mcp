# Mattermost MCP

A standalone Go CLI and local stdio bridge for the **official Mattermost MCP server**. Use stable commands for built-in tools, discover the catalog your server enables, or expose those tools to a local MCP client.

The command structure follows [openproject-mcp](https://github.com/pinealctx/openproject-mcp): Cobra commands, `-o text|json`, environment-based configuration, a root Go binary, and prebuilt releases. It uses the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).

This is an independent client, not an official Mattermost product. It needs an enabled Mattermost Agents HTTP MCP endpoint. It does not install plugins, change server configuration, or run an LLM.

## Install

Download a prebuilt binary from [Releases](https://github.com/pinealctx/mattermost-mcp/releases/latest), verify it against `checksums.txt`, and place it on your `PATH`. Packages are available for Linux and macOS (amd64/arm64), and Windows (amd64).

With Go 1.26.1 or later:

```sh
go install github.com/pinealctx/mattermost-mcp@latest
```

## Quick start

Set your server address and inject a PAT or existing login session token as `MATTERMOST_TOKEN` using your preferred credential manager or environment:

```sh
export MATTERMOST_URL=https://chat.example.com
mattermost-mcp connection test
mattermost-mcp user me -o json
mattermost-mcp tool list
mattermost-mcp tool describe search_posts
mattermost-mcp search 'in:engineering deployment' -o json
```

For password login, unset `MATTERMOST_TOKEN` and supply a username. In an interactive terminal, missing passwords and MFA codes are prompted without echo:

```sh
mattermost-mcp --url https://chat.example.com --username alice connection test
```

The password is sent only to the server's `/api/v4/users/login` endpoint. Its session token authenticates MCP calls. This does **not** create a PAT or require PAT creation permission. Ordinary password-authenticated commands log out on completion. When starting a history worker, a temporary session is handed to that worker for cleanup on normal exit. It never logs out a token supplied by the caller unless `auth logout` is explicitly invoked.

Accounts must permit password login. MFA remains enforced. SSO-only accounts require a separately obtained session/OAuth token; browser OAuth login is not implemented in this release. No password guessing, automatic login retries, background credential refresh, or desktop-cookie extraction is performed.

## Configuration

Flags override their corresponding environment values. A supplied token takes precedence over username/password authentication. No configuration files, OS keychain APIs, organization-specific secret providers, or agent skills are read by this program.

| Environment variable | Flag | Meaning |
| --- | --- | --- |
| `MATTERMOST_URL` | `--url` | Required base URL; no built-in deployment |
| `MATTERMOST_MCP_URL` | `--mcp-url` | Optional endpoint override on the same origin |
| `MATTERMOST_TOKEN` | `--token` | PAT, OAuth access token, or login session token |
| `MATTERMOST_USERNAME` | `--username` | Username, email, or supported LDAP login ID |
| `MATTERMOST_PASSWORD` | `--password-stdin` | Password from the environment or stdin; never a password argument |
| `MATTERMOST_MFA_TOKEN` | — | Current one-time MFA code, if required |
| `MATTERMOST_TIMEOUT` | `--timeout` | Timeout such as `30s` or `2m`; default `30s` |
| `MATTERMOST_CACHE` | `--cache` | `prefer` (default), `refresh`, or `off` |
| `MATTERMOST_CACHE_DIR` | — | Optional absolute, private directory for snapshots |
| `MATTERMOST_HISTORY_DIR` | — | Optional absolute private directory for account SQLite databases |
| `MATTERMOST_ENABLED_TOOLS` | `mcp --enabled-tools` | Optional comma-separated stdio tool allowlist |

The default MCP URL is `<base-url>/plugins/mattermost-ai/mcp-server/mcp`. Base URLs with a deployment path prefix are supported. HTTPS is required except on loopback development servers. Credentials are never forwarded across origins, and HTTP redirects are rejected. Use the final server URL.

Prefer environment injection over `--token`, which may be visible in process listings. Do not place passwords or tokens in shell history or checked-in files. The tool has no knowledge of how environment values were obtained.

## Session lifecycle

```sh
# Validate password login only and log out immediately.
mattermost-mcp --username alice auth login --no-sync

# Check injected credentials without printing them.
mattermost-mcp auth status -o json

# End an injected login session. This does not revoke a PAT.
mattermost-mcp auth logout
```

For repeated commands with MFA, an external credential manager can retain a session instead of prompting on every command:

```text
mattermost-mcp --username alice auth login --token-only
```

**`--token-only` intentionally emits a secret to stdout** and leaves that login session active. Direct stdout to your credential manager, not a chat, log, or ordinary document. It is the only command that exports credentials. Password/MFA prompts go to stderr. If output fails, the tool attempts to log out rather than hand over an inaccessible session. Inject the saved value as `MATTERMOST_TOKEN` for subsequent commands. Explicitly log out when done; session expiry and administrator revocation still apply. The CLI does not save or automatically refresh it.

## Stable commands and availability

The binary includes fixed names and parameter flags for 121 built-in registrations from [public Agents v2.11.0](https://github.com/mattermost/mattermost-plugin-agents/tree/f81a90b2b0c0b388d19a81ade9772b2506cf8f9a/mcpserver/tools), including optional and development tools. Built-in command help works without credentials. On execution the client checks the current account's server-provided catalog. The server controls availability, input validation, and authorization. Plugin-specific or newer tools remain accessible through `tool call`.

```sh
mattermost-mcp tool get-user-channels --help
mattermost-mcp tool get-user-channels --team-id TEAM_ID --page 0 --per-page 100
mattermost-mcp channel open CHANNEL_ID --limit 20
mattermost-mcp tool list --cache refresh
```

Underscore tool names are aliases. Scalar parameters have typed flags; arrays and objects take JSON. Use `--args-file FILE` or `--args-file -` as an alternative for the full argument object, version-specific fields or sensitive inputs. Flags that would overlap with global settings use the `--arg-` prefix (e.g. `--arg-username`). Password fields use file/stdin input. Local-filesystem-only parameters from the official plugin are excluded from the fixed HTTP flags.

## Local snapshots

The CLI and stdio bridge share an on-demand cache. A database belongs to one server URL (including installation path), MCP endpoint, and **server-verified user ID**. Separate injected tokens for the same account have separate cache keys because their scopes can differ. Password logins for the same account share a password-session partition. Token strings and tool argument objects are not stored as cache keys. The CLI authenticates on each invocation before serving a snapshot. A warm hit can skip both MCP initialization and the tool request. This is an online cache, not an offline account selector.

| Data | Reuse window |
| --- | --- |
| Dynamic tool catalog (names, schemas, annotations) | 5 minutes |
| Team, channel, membership and user lookup results on the allowlist | 2 minutes |
| Message and search tool calls | Live MCP calls; use `channel open` for the SQLite timeline |
| Other tools | Live |

Pass `--cache-trace` to show which eligible reads came from the server (`live`) or disk (`hit`) on stderr. Catalog and tool-result events are separate. Arguments and result contents are omitted.

Snapshots match the exact query, including pagination, filters and requested limits. The SQLite message history described below is independent of these snapshots. Writes in other clients and changes to channel membership/visibility can make an unexpired snapshot stale. Use `--cache refresh` for an authoritative read and to replace the snapshot. `connection test` always verifies live connectivity. The bridge registers a catalog snapshot at startup; restart with `--cache refresh` to immediately pick up a changed toolset.

With caching enabled, writes and unrecognized calls always go upstream and invalidate snapshots for the account before and after the attempt. A read that started before invalidation cannot repopulate the old generation. `--cache off` bypasses disk, including invalidation, so other processes may retain snapshots until expiry or explicit refresh. `cache clear` invalidates the current account's snapshots across its credential partitions.

Files live under the OS user cache directory in `mattermost-mcp/`, or the selected private cache directory. On Unix the application directory is private (0700) and new database files use 0600. The database contains **unencrypted** cached responses. Expiry limits reuse; physical removal is separate from expiry or logical clearing. Each account holds at most 128 snapshots, each at most 1 MiB. To remove the store physically, close clients and remove the dedicated cache directory. The legacy `--cache-messages` option is accepted for compatibility. The CLI no longer writes message-result TTL snapshots. Existing v0.2 snapshot files are not imported.

## Persistent message history

`auth login` initializes the most recent page of each joined conversation (including direct/group channels), then starts a detached worker to fill older pages. The worker finishes after backfill and reconciliation. Re-running login or `sync resume` resumes saved progress. A preconfigured bearer works with `sync start`. For password-only validation without syncing use `auth login --no-sync`; to initialize a single channel use `auth login --sync-channel CHANNEL_ID`. Token export still emits only the secret to stdout.

```sh
mattermost-mcp sync start
mattermost-mcp sync start --channel CHANNEL_ID --foreground
mattermost-mcp channel open CHANNEL_ID --limit 30
mattermost-mcp sync status --channel CHANNEL_ID -o json
mattermost-mcp sync stop --channel CHANNEL_ID
mattermost-mcp sync resume --channel CHANNEL_ID
```

- SQLite databases under the OS user configuration directory (`mattermost-mcp/history`) are partitioned by the verified server URL and stable user ID. Later sessions for the same account reuse its history. Files are plaintext in a private directory (0700 and new database files 0600 on Unix). Rows store post ID, channel, author, thread root, timestamps, type, text and file IDs. Attachment bodies are not downloaded.
- `channel open` emits two JSON lines, `local` then `reconciled`, newest first. The local phase checks current channel read permission and membership, then emits rows from SQLite before downloading new posts. Rows observed under a different credential are revalidated before display. A local preview may briefly show an older edit or a since-deleted post; use the reconciled frame for current content. Opening requires connectivity. Then it fetches the recent window and modifications since the saved watermark, merges edits, removes deleted bodies, and resumes unfinished backfill. An empty local view is expected on first use.
- Normal MCP reads such as `tool read-channel` keep their upstream result format. Message history does not use the metadata cache TTL. `--cache off` affects the metadata cache, not an explicit history command.
- A page and its checkpoint commit in one transaction. Historical pagination uses a fixed anchor when possible and verifies page boundaries. A moving boundary restarts the sweep. At traversal end, unseen IDs are checked against the server, not assumed deleted. A saturated changes response schedules a full reconciliation without advancing past the gap. History that the server has made inaccessible cannot be recovered.
- A completed sweep describes the history visible during the last pass. The client pulls changes on open, rechecks displayed rows, and schedules a maintenance sweep on the next use after 24 hours. It does not subscribe to WebSocket events while idle. A full-account discovery prunes channels no longer joined; a scoped operation only touches its requested channel.
- Jobs use page-sized transactions, a short delay between steps and backoff on throttling. `sync status` shows per-channel progress and the job outcome. `sync stop` requests a stop at a page boundary. After interruption or token expiry, supply valid credentials and run `sync resume`. Only the bearer is transferred through a private pipe to a worker, never saved to a job file. Externally supplied sessions stay owned by their caller.

## Commands

| Command | Purpose |
| --- | --- |
| `connection test` | Initialize MCP, enumerate tools, and call `get_me` |
| `tool list` | Fetch the complete paginated tool catalog |
| `tool describe NAME` | Show the current schema, description and annotations |
| `tool call NAME --args '{...}'` | Call any upstream tool once |
| `tool call NAME --args-file request.json` | Read JSON arguments from a file; `-` means stdin |
| `user me` / `user get USER_ID` | Read users |
| `team list` / `team get NAME` | Read teams |
| `channel list TEAM_ID` / `channel get CHANNEL_ID` | Read channels |
| `channel read CHANNEL_ID --limit 20` | Read recent messages |
| `post get POST_ID` | Read a post and its thread |
| `post create CHANNEL_ID --message-file message.md` | Send a message; add `--root-id POST_ID` to reply |
| `search QUERY` | Search messages; optional `--team-id` / `--channel-id` |
| `auth login` / `auth status` / `auth logout` | Manage authentication |
| `mcp` (or no subcommand) | Run the local stdio bridge |

`tools` is an alias for `tool`. Domain commands are convenience mappings to official tools. Available tools and schemas vary by server/plugin version and user permissions; `tool describe` and `tool call` provide the full upstream surface without a client release. Input identifiers are not silently resolved to potentially ambiguous channels or users.

Use files for multiline messages and large JSON requests. An argument file must contain one JSON object. Stdin cannot simultaneously supply a password and an argument/message file.

Newer Agents versions require display names to verify the destination of a post. Read `tool describe create_post` for your server, then supply `--channel-display-name 'Engineering'` and, for public/private channels, `--team-display-name 'Example Team'` with `post create`. Direct/group message channels have no team. For pagination and other parameters beyond convenience flags, use `tool call` with the live schema.

JSON tool output preserves the upstream MCP result envelope (`content`, optional `structuredContent`, and `isError`). Text output prints text content; non-text content remains JSON. A tool-level `isError` also produces a nonzero exit status. A timeout after sending a write can have an unknown outcome: inspect the server before trying again. The CLI does not retry tool writes automatically.

## Local MCP bridge

Launch the binary with the same standard environment variables. It discovers tools from the official HTTP endpoint and forwards their input/output schemas, annotations and results over local stdio:

```sh
mattermost-mcp mcp
mattermost-mcp mcp --enabled-tools get_me,read_channel,read_post,search_posts
```

A local MCP client's generic configuration can use:

```json
{
  "mcpServers": {
    "mattermost": {
      "command": "mattermost-mcp",
      "args": ["mcp"],
      "env": {"MATTERMOST_URL": "https://chat.example.com"}
    }
  }
}
```

Arrange credential injection through the launch environment; do not commit credential values to client configuration. Stdio mode never prompts or consumes stdin as a password stream. It uses stdout exclusively for MCP. The catalog is a startup snapshot: restart after server-side tool changes. No local HTTP listener is exposed. Only tools are bridged; sampling, elicitation, resources, prompts, and unsolicited server notifications are not forwarded. The optional standalone notification SSE stream is disabled; SSE responses to tool requests remain supported.

The program enforces no organization-specific communication or approval workflow. Calling a write tool performs that operation using the authenticated user's server permissions. Such workflows belong to the invoking application.

## Development

```sh
make build
make test
make lint
make release VERSION=v0.1.0
```

Tests use isolated local HTTP and MCP fixtures, including password/MFA login, session ownership and cleanup, pagination, JSON/SSE responses, stdio bridging, tool errors, and credential origin boundaries. They require no real account or deployment. Release tags build checksummed archives for five platform targets.

MCP tools and writes use the official MCP endpoint. Login and message synchronization use the authenticated same-origin Mattermost REST API. See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidance and [SECURITY.md](SECURITY.md) for reporting security issues.

Licensed under the [MIT License](LICENSE).
