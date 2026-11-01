# Security

Report vulnerabilities through this repository's GitHub **Security → Report a vulnerability** feature. Do not include live credentials, private deployment addresses, or private message contents in public issues.

The current stable release is supported. Credentials are supplied by the caller, held in memory, and used only on the configured server origin. TLS verification is enabled. No credential store or server address is built in. `auth login --token-only` is an explicit credential export and must be directed to a trusted destination.

Server permissions and policies remain authoritative. MCP tool descriptions and message contents are untrusted data for an AI consumer. The CLI does not interpret that content as local instructions or execute returned shell commands.
