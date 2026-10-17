# Security

This CLI handles Expensify device credentials and financial records. Report vulnerabilities privately. Keep tokens, device secrets, receipts, and account data out of public issues and pull requests.

## Report a vulnerability

If the repository's **Security** tab offers **Report a vulnerability**, use [GitHub's private reporting form](https://github.com/magarcia/expensify-cli/security/advisories/new). That form depends on the maintainer enabling private vulnerability reporting.

If private reporting isn't available, open an issue asking the maintainer to enable a private reporting channel. Include no vulnerability details, credentials, or exploit code in that issue.

In the private report, include:

- The CLI version, operating system, and architecture.
- The affected command or component and the security impact.
- Minimal reproduction steps using synthetic data.
- Any proposed fix or workaround.

Test only accounts and systems you're authorized to access. If you discover exposed credentials, treat them as compromised and revoke or replace them with the provider. Deleting a public message or running local logout doesn't revoke an Expensify device session.

## Supported versions

Security fixes target the latest release. Before the first release, they target `main`. Older versions don't receive separate security backports. Use the latest available release and review its release notes.

## Credential handling

The default `auto` storage mode prefers the OS keyring through `go-keyring`: macOS Keychain, Windows Credential Manager, or Linux Secret Service. If a keyring write is unavailable, the CLI can save credentials in a private plaintext JSON file. Set `EXPENSIFY_CREDENTIAL_STORE=keyring` to require keyring storage and prevent plaintext fallback.

Browser login starts a separate Chrome-compatible browser profile. The CLI reads credentials through a local Chrome DevTools connection, closes the browser it started, and removes that temporary profile when login ends. An abrupt process or machine failure can interrupt cleanup.

`expensify auth status` reports the storage backend without showing secrets. `expensify auth logout` removes local credentials for the selected configuration directory; it doesn't revoke the device session at Expensify. See [authentication](docs/authentication.md) for storage permissions, imports, renewal, and cleanup behavior.

Public web-client partner identifiers in the source identify the Expensify app's client. They aren't your account credentials. An `authToken`, device login, device secret, or browser storage capture can grant account access and must stay private.
