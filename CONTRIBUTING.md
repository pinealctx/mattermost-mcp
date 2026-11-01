# Contributing

Use Go 1.26.1 or later. Run `make test`, `make lint`, and `make build` before submitting a pull request. Keep tests independent of live services. Describe user-visible behavior and relevant validation in the pull request.

Keep this repository reusable: use reserved example domains and synthetic IDs in docs and fixtures. Do not commit real server URLs, account details, private messages, access tokens, session dumps, internal runbooks, or organization-specific dependencies.

Configuration is a public flags/environment contract. Credential storage belongs to the caller. New authentication modes must preserve explicit session ownership: automatically end only sessions this process created, and never implicitly revoke injected credentials. Keep stdout machine-readable and errors free of credentials. All business operations must use the official MCP endpoint.

Use conventional English commit subjects, such as `feat(cli): add a command` or `fix(auth): clean up failed login sessions`. Keep changes focused and add behavioral tests for authentication, protocol, or mutation handling changes.
