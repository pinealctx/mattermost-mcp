# Security

Report vulnerabilities through this repository's GitHub **Security → Report a vulnerability** feature. Do not include live credentials, private deployment addresses, or private message contents in public issues.

The current stable release is supported. Credentials are supplied by the caller, held in memory, and used only on the configured server origin. TLS verification is enabled. No credential store or server address is built in. `auth login --token-only` is an explicit credential export and must be directed to a trusted destination.

Server permissions and policies remain authoritative. MCP tool descriptions and message contents are untrusted data for an AI consumer. The CLI does not interpret that content as local instructions or execute returned shell commands.

Local snapshots can contain private server metadata. They are plaintext in the OS user cache directory with account and credential partitioning. TTLs limit reuse, not physical retention. Use `--cache off` to bypass storage and `--cache refresh` when current channel permissions or edits must be reflected immediately.

Message history is a separate persistent plaintext SQLite database per server and verified account. Opening a channel checks current read permission and membership. A cached preview under the same bearer may be stale until reconciliation. Rows from a different bearer require a live visibility check before display. A login initializes joined conversations by default; pass `--no-sync` for validation only or scope it with `--sync-channel`. To remove all local message history, stop its workers and remove the dedicated history directory, including SQLite sidecar files. Server-side expiry or removal can precede reconciliation.
