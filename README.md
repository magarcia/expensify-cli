# expensify

[![CI](https://github.com/magarcia/expensify-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/magarcia/expensify-cli/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/magarcia/expensify-cli)](https://github.com/magarcia/expensify-cli/releases/latest)
[![MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

New Expensify from your terminal. Manage expenses, receipts, and reports with one
binary, readable tables, and structured output for scripts and agents.

Unofficial client for [New Expensify](https://new.expensify.com).
Its app API can change without notice.

## Install

On macOS or Linux, use [Homebrew](https://brew.sh):

```sh
brew install magarcia/tap/expensify
```

[Download binaries](https://github.com/magarcia/expensify-cli/releases/latest)
for macOS, Linux, and Windows on amd64 and arm64. See
[installation](docs/installation.md) for checksums, completions, source builds,
and upgrades.

## Get started

```sh
expensify auth login
expensify workspace list
expensify expense list --limit 20
```

Login opens a separate Chrome-compatible window for sign-in. Credentials use your
OS keyring when available, with a private plaintext file as fallback. See
[authentication](docs/authentication.md) for storage controls and other login methods.

Find a draft report, then preview an expense with a receipt. Replace the workspace
and report IDs and receipt path with your own:

```sh
expensify report list --policy-id YOUR_POLICY_ID --status drafts
expensify expense create --report-id YOUR_REPORT_ID \
  --amount 1250 --currency USD --merchant Cafe \
  --receipt receipt.pdf --dry-run
```

Amounts use currency minor units: `1250` is `12.50 USD`. `--dry-run` validates local
inputs without calling Expensify; remove it to create the expense. See the
[monthly draft workflow](docs/automation.md#monthly-draft-workflow) for report
creation, Notes, and workspace expenses.

Discover commands and flags with `expensify --help` or
`expensify expense create --help`.

## Agents and scripts

Use `--no-input --output llm` for agents or `--output json` for scripts:

```sh
expensify expense list --no-input --output llm
```

Install the [agent skill](skills/expensify/SKILL.md) to teach an agent how to learn
the CLI through its help:

```sh
gh skill install magarcia/expensify-cli expensify --agent codex --scope user
```

See [skill installation](docs/installation.md#agent-skill) for other agents and the
Vercel skills installer.

## Documentation

- [Installation](docs/installation.md): platforms, downloads, and shell completions.
- [Authentication](docs/authentication.md): login, credentials, and session renewal.
- [Automation](docs/automation.md): workflows, output formats, configuration, and errors.
- [Contributing](CONTRIBUTING.md): build with Go and `just`, run tests, and contribute.

[Report a bug](https://github.com/magarcia/expensify-cli/issues) ·
[Security](SECURITY.md) · [MIT license](LICENSE)
