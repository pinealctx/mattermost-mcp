# Mattermost MCP

A standalone Go CLI and local stdio bridge for the **official Mattermost MCP server**. Discover its live tool catalog, call tools from scripts, or expose those tools to a local MCP client.

The command structure follows [openproject-mcp](https://github.com/pinealctx/openproject-mcp): Cobra commands, `-o text|json`, environment-based configuration, a root Go binary, and prebuilt releases. It uses the official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk).

This is an independent client, not an official Mattermost product. It needs an enabled Mattermost Agents HTTP MCP endpoint. It does not install plugins, change server configuration, implement a second tool catalog, or run an LLM.

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

The password is sent only to the server's `/api/v4/users/login` endpoint. Its session token authenticates MCP calls. This does **not** create a PAT or require PAT creation permission. Each password-authenticated command logs out its own session on completion, including errors and normal interrupt handling. It never logs out a token supplied by the caller unless `auth logout` is explicitly invoked.

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
| `MATTERMOST_ENABLED_TOOLS` | `mcp --enabled-tools` | Optional comma-separated stdio tool allowlist |

The default MCP URL is `<base-url>/plugins/mattermost-ai/mcp-server/mcp`. Base URLs with a deployment path prefix are supported. HTTPS is required except on loopback development servers. Credentials are never forwarded across origins, and HTTP redirects are rejected. Use the final server URL.

Prefer environment injection over `--token`, which may be visible in process listings. Do not place passwords or tokens in shell history or checked-in files. The tool has no knowledge of how environment values were obtained.

## Session lifecycle

```sh
# Validate password login and log out immediately (no credential output).
mattermost-mcp --username alice auth login

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

Arrange credential injection through the launch environment; do not commit credential values to client configuration. Stdio mode never prompts or consumes stdin as a password stream. It uses stdout exclusively for MCP. The catalog is a startup snapshot: restart after server-side tool changes. No local HTTP listener is exposed. Only tools are bridged; sampling, elicitation, resources, and prompts are not forwarded.

The program enforces no organization-specific communication or approval workflow. Calling a write tool performs that operation using the authenticated user's server permissions. Such workflows belong to the invoking application.

## Development

```sh
make build
make test
make lint
make release VERSION=v0.1.0
```

Tests use isolated local HTTP and MCP fixtures, including password/MFA login, session ownership and cleanup, pagination, JSON/SSE responses, stdio bridging, tool errors, and credential origin boundaries. They require no real account or deployment. Release tags build checksummed archives for five platform targets.

The internal authentication adapter calls REST only for login, account validation and logout. Business operations use the official MCP endpoint. See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution guidance and [SECURITY.md](SECURITY.md) for reporting security issues.

Licensed under the [MIT License](LICENSE).
