# expensify

New Expensify from your terminal. Track expenses, attach receipts, manage reports, and inspect workspaces with one Go binary. Use terminal tables for everyday work, or structured output for scripts and agents.

This is an unofficial client for the New Expensify app API. It's independent of Expensify, and the app endpoints can change without notice. It uses New Expensify device sessions; Integration Server partner credentials won't work.

## Install

On macOS or Linux, install with [Homebrew](https://brew.sh):

```sh
brew install magarcia/tap/expensify
```

Homebrew selects the binary for your OS and architecture and installs shell completions. Upgrade with `brew update` followed by `brew upgrade expensify`. No Go installation is needed.

### Other installation methods

Download the archive for your platform from [GitHub Releases](https://github.com/magarcia/expensify-cli/releases), verify its SHA-256 checksum, and put `expensify` (`expensify.exe` on Windows) on your `PATH`. Release binaries need no Go installation.

| Platform | Intel / AMD 64-bit | ARM 64-bit |
| --- | --- | --- |
| macOS | `darwin_amd64.tar.gz` | `darwin_arm64.tar.gz` |
| Linux | `linux_amd64.tar.gz` | `linux_arm64.tar.gz` |
| Windows | `windows_amd64.zip` | `windows_arm64.zip` |

Asset names start with `expensify_vX.Y.Z_`. Apple Silicon uses `arm64`; `x86_64` uses `amd64`. See [installation](docs/installation.md) for download commands, checksum verification, shell completions, and source builds.

With Go 1.26 or newer, you can also install the public module:

```sh
go install github.com/magarcia/expensify-cli/cmd/expensify@latest
```

This installs to `GOBIN`, or the first `GOPATH` entry's `bin` directory.

## Log in

```sh
expensify auth login
expensify auth status --check
expensify workspace list
expensify expense list --limit 20
```

Login opens a separate Chrome-compatible browser window. Complete sign-in there; the CLI captures the device credentials and verifies them with Expensify. It uses a temporary browser profile, so you'll sign in even if your usual browser is already authenticated.

Credentials go into macOS Keychain, Windows Credential Manager, or Linux Secret Service when available. The default falls back to a private plaintext file if the keyring is unavailable. Set `EXPENSIFY_CREDENTIAL_STORE=keyring` to require the OS keyring. `expensify auth status` reports the selected storage without revealing secrets.

Choose a browser with `expensify auth login --browser chrome`, or use `--manual` for hidden credential prompts. See [authentication](docs/authentication.md) for supported browsers, credential imports, headless use, and token renewal.

## Everyday commands

With no default workspace configured, track a personal expense. Amounts use integer currency minor units: `1250` means `12.50 USD`.

```sh
expensify expense create \
  --amount 1250 --currency USD \
  --merchant Cafe --date 2026-10-17 \
  --comment 'Coffee with the team'
```

Omit `--date` to use today's local date. Currency defaults to your configured currency, initially `USD`.

Use `expense create --policy-id YOUR_POLICY_ID` to create an expense through your workspace's policy chat and a draft report. Use `--report-id REPORT_ID` to choose your own existing draft; the CLI infers its workspace. If you pass both IDs, they must identify the same workspace. With neither flag, a configured `policy-id` default selects the workspace; without that default, creation stays personal.

Filter expenses, view details, and attach a receipt:

```sh
expensify expense list --status unreported --since 2026-10-01 --merchant Cafe
expensify expense view --expense-id 123
expensify expense edit --expense-id 123 --category Meals
expensify expense attach --expense-id 123 --file receipt.jpg
```

Receipts must be JPEG, PNG, or PDF files up to 32 MiB, with matching extensions. Edits change only the fields you supply. Pass an empty `--comment`, `--category`, or `--tag` to clear that field, for example `--comment=''`.

Find or create a monthly draft:

```sh
expensify report list --policy-id YOUR_POLICY_ID --status drafts

# Create this report only if you don't already have the draft you need.
expensify report create --name 'October expenses' --policy-id YOUR_POLICY_ID \
  --notes 'Health, Software License, Books, Courses & Internet'
expensify report view --report-id 456
```

Report creation checks the workspace policy and refuses settings known to submit a new report instantly. Other workspace submission schedules still apply. Use the returned report ID instead of `456`, and check that the report is still your draft.

Set a report's Notes with `report edit --report-id 456 --notes 'Monthly reimbursements'`. Notes use the workspace's text report field named Notes. The CLI edits your own draft expense reports; `report create --notes` also requires `--policy-id`. Pass `--notes=''` to clear optional Notes; required fields reject blank text. `report view` always returns a `notes` string, including `""` for empty Notes.

Add an expense and its receipt to that draft, then read it back:

```sh
expensify expense create --report-id 456 \
  --amount 1250 --currency USD --merchant Cafe --date 2026-10-17 \
  --receipt receipt.jpg --category Meals --tag Project
expensify expense list --report-id 456
expensify report view --report-id 456
```

Choose category and tag names from `category list --policy-id YOUR_POLICY_ID` and `tag list --policy-id YOUR_POLICY_ID`. `--receipt` sends a JPEG, PNG, or PDF file up to 32 MiB with the initial create request; you don't need a separate attach command.

Move an existing expense with `expense move --expense-id 123 --report-id 456`. The destination must be your own draft. The source must be your own unreported expense or an expense in your own draft in the same workspace. The CLI rejects submitted reports, another owner's reports, and workspace mismatches.

The CLI asks Expensify to defer immediate submission when creating a workspace expense. Your workspace's scheduled submission rules still apply. See the [monthly draft workflow](docs/automation.md#monthly-draft-workflow) for explicit checks and unattended commands. These steps don't submit the report.

Date boundaries are inclusive. Expense and report lists accept `drafts`, `outstanding`, `approved`, `done`, or `paid`; expenses also accept `unreported` and `deleted`.

Preview any resource mutation with `--dry-run`. It validates local input without authenticating or calling Expensify, so it can't verify report state, ownership, permissions, or server behavior:

```sh
expensify report submit --report-id 456 --dry-run
```

Deletion, report submission, and report approval require confirmation. For an unattended command, pass `--yes` after reviewing the action. The CLI doesn't automatically retry transport or server failures, because a write might already have succeeded.

| Resource | Commands |
| --- | --- |
| `expense` | `list`, `view`, `create`, `edit`, `move`, `delete`, `attach` |
| `report` | `list`, `view`, `create`, `edit`, `submit`, `approve`, `comment` |
| `workspace` | `list`, `view` |
| `category` | `list` |
| `tag` | `list` |
| `user` | `view` |
| `auth` | `login`, `status`, `logout` |
| `config` | `list`, `get`, `set` |

Run `expensify <resource> <command> --help` for flags and examples. Help works without authentication.

## Scripts and agents

Agents can use the short, help-driven [Expensify skill](skills/expensify/SKILL.md), also included in release archives.

Choose one installer to make the skill available to Codex across projects:

```sh
gh skill install magarcia/expensify-cli expensify --agent codex --scope user

# Or, with Node.js and npm:
npx skills add magarcia/expensify-cli --skill expensify --agent codex --global
```

See [skill installation](docs/installation.md#agent-skill) for other agents, project scope, and local checkouts. Remote installation requires the repository to be published on GitHub.

Use `--output llm` for agents: it produces labeled records with field names, plus pagination metadata for expense and report lists. Use `--output json` for scripts that consume JSON. `--output auto` produces terminal tables on a TTY and tab-separated output when piped. Command data goes to stdout; diagnostics and errors go to stderr.

```sh
expensify expense list --limit 20 --no-input --output json
expensify report view --report-id 456 --no-input --output llm
```

Agents should pass `--no-input --output llm`. Mutations that require confirmation also need `--yes`. Resource JSON uses string IDs, integer minor-unit amounts, and `snake_case` fields. See [automation](docs/automation.md) for pagination, exit codes, structured errors, and write recipes.

## Configuration

```sh
expensify config set --key currency --value EUR
expensify config set --key policy-id --value YOUR_POLICY_ID
expensify config list
```

Settings resolve in this order: flags, environment, `.expensify.json` in the current directory, user settings, defaults. Supported keys are `output`, `policy-id`, `currency`, and `timeout`. User settings live in `~/.config/expensify/config.json`, respecting an absolute `XDG_CONFIG_HOME`; `--config-dir` and `EXPENSIFY_CONFIG_DIR` override the directory.

Run `expensify help environment` for environment variables, `expensify help exit-codes` for exit codes, or `expensify --help` for global flags. See [authentication](docs/authentication.md#local-storage) for credential storage and its separate resolution rules.

## Development and releases

Install Go 1.26 or newer, Python 3, and [casey/just](https://github.com/casey/just), then run:

```sh
just build
./bin/expensify --version
just check
```

`just check` checks formatting, runs `go vet`, tests, packaging tests, and license checks, then builds the binary. `just test-race` checks for data races. `just release-snapshot` cross-compiles all six release targets and validates the archives and checksums. Run `just` to list recipes.

GitHub Actions runs checks on pull requests and pushes. Version tags publish cross-platform binaries, checksums, and generated release notes. See [contributing](CONTRIBUTING.md) for the development workflow and [releasing](docs/releasing.md) for maintainer setup and release steps.

Tests exercise HTTP fixtures, credential storage, token renewal, command output, exit codes, and subprocess behavior. They don't prove that current Expensify endpoints or an account's permissions match the fixtures. Verify a real account with `auth status --check` and a read command before making writes.

## Help and license

Report reproducible bugs or propose features in [GitHub Issues](https://github.com/magarcia/expensify-cli/issues). Keep credentials, receipts, and personal expense data out of reports. Use the private process in [SECURITY.md](SECURITY.md) for vulnerabilities.

Released under the [MIT license](LICENSE). Dependency licenses and notices are listed in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).
