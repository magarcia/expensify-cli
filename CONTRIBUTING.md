# Contributing

Start with an issue for a new command, API change, or dependency. Include the workflow you're trying to support and a concrete command example. Small bug fixes and documentation corrections can go straight to a pull request.

This is an unofficial client for New Expensify's app API. Check the public [Expensify app source](https://github.com/Expensify/App) when an endpoint or response changes. Keep credentials, receipts, and account data out of issues, fixtures, and pull requests.

## Set up

Install [Go](https://go.dev/doc/install) 1.26 or newer, [casey/just](https://github.com/casey/just), and Python 3. Windows contributors also need Bash, available with [Git for Windows](https://gitforwindows.org/).

```sh
git clone https://github.com/magarcia/expensify-cli.git
cd expensify-cli
just build
./bin/expensify --version
just check
```

`just build` downloads Go dependencies as needed. Tests use synthetic responses and isolated configuration. They don't require an Expensify account or a working desktop keyring.

## Make a change

Keep changes scoped to one problem. Use `gofmt` and follow the surrounding Go code. Prefer clear names and small interfaces; add comments for constraints or intent that the code can't explain.

| Location | Responsibility |
| --- | --- |
| `cmd/expensify` | Process entry point, signals, and exit behavior |
| `internal/cli` | Commands, flags, confirmation, and help |
| `internal/expensify` | Resource validation, API operations, and typed results |
| `internal/api` | HTTP requests, authentication, and token renewal |
| `internal/browserauth` | Isolated browser login and credential capture |
| `internal/config` | Settings, keyring access, and private file storage |
| `internal/output`, `internal/fault` | Output formats, diagnostics, and exit codes |
| `integration` | CLI behavior across command, API, and storage boundaries |
| `scripts/build` | Build metadata and release archives |

Keep raw API response handling in the resource and HTTP layers. Human output, JSON output, and error codes are interfaces callers depend on. When they change, update help, examples, and [automation documentation](docs/automation.md) together.

For bug fixes, add a test that reproduces the failure. Use synthetic or fully sanitized fixtures. Cover meaningful boundaries: malformed API values, token renewal, output contracts, and failures during credential storage. Keep automated tests independent of personal accounts and production writes.

Authentication changes need to preserve account isolation, avoid printing secrets, and handle cancellation. See [authentication](docs/authentication.md) for the storage and browser lifecycle contract. Report vulnerabilities privately using [SECURITY.md](SECURITY.md).

## Verify

Run focused tests while working, then the project checks:

```sh
just fmt
just check
just test-race
```

For build or archive changes, also run:

```sh
just packaging-test
just release-snapshot
```

`just release-snapshot` builds macOS, Linux, and Windows archives for amd64 and arm64. It verifies compilation for those targets; CI runs native tests on macOS, Linux, and Windows.

After changing Go dependencies, run `just notices` and commit the updated `THIRD_PARTY_NOTICES`. The notice check covers runtime dependencies across all six release targets, including OS-specific keyring libraries.

A live API check is separate from the fixture suite. If you choose to run one, use your own account and start with `expensify auth status --check` and a read command. Preview mutations with `--dry-run` before deliberately sending them. Don't add account-dependent checks to CI.

## Open a pull request

Explain the problem, the resulting behavior, and the checks you ran. Include a sanitized before/after example when output changes. Update the README or relevant guide when commands, configuration, authentication, or build steps change.

By submitting a contribution, you agree to license it under this project's [license](LICENSE).
