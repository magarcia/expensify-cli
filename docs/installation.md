# Installation

Release binaries cover macOS, Linux, and Windows on amd64 and arm64. They don't need Go or a separate language runtime. Browser login needs an installed supported Chrome-compatible browser; keyring storage needs the platform services described below.

## Choose an archive

Open [GitHub Releases](https://github.com/magarcia/expensify-cli/releases) and choose a published version. If no releases exist yet, use [a source build](#build-from-source).

| Your platform | Release archive |
| --- | --- |
| macOS, Intel | `expensify_vX.Y.Z_darwin_amd64.tar.gz` |
| macOS, Apple Silicon | `expensify_vX.Y.Z_darwin_arm64.tar.gz` |
| Linux, Intel / AMD 64-bit | `expensify_vX.Y.Z_linux_amd64.tar.gz` |
| Linux, ARM 64-bit | `expensify_vX.Y.Z_linux_arm64.tar.gz` |
| Windows, Intel / AMD 64-bit | `expensify_vX.Y.Z_windows_amd64.zip` |
| Windows, ARM 64-bit | `expensify_vX.Y.Z_windows_arm64.zip` |

On macOS or Linux, `uname -m` reports the architecture: `x86_64` maps to `amd64`; `arm64` or `aarch64` maps to `arm64`. On Windows, check **Settings > System > About > System type**. There are no 32-bit archives.

Use the named binary asset, not GitHub's automatic **Source code** archives. Each binary archive includes the executable, documentation, license notices, and shell completions.

## macOS and Linux

The example below selects macOS on Apple Silicon. Replace `version` with a published tag, and select `os` and `arch` from the table. `v0.1.0` is an example, not a promise that this version has been published.

```sh
mkdir -p expensify-download
cd expensify-download

version=v0.1.0
os=darwin
arch=arm64
archive="expensify_${version}_${os}_${arch}.tar.gz"
base="https://github.com/magarcia/expensify-cli/releases/download/${version}"

curl --fail --location --output "$archive" "$base/$archive"
curl --fail --location --output checksums.txt "$base/checksums.txt"
awk -v file="$archive" '$2 == file' checksums.txt > selected-checksum.txt
```

Verify the downloaded archive before extracting it. On macOS:

```sh
shasum -a 256 --check selected-checksum.txt
```

On Linux:

```sh
sha256sum --check selected-checksum.txt
```

Continue only if verification succeeds and prints `OK` for your archive. Install the binary into your user directory:

```sh
tar -xzf "$archive"
install -d "$HOME/.local/bin"
install -m 755 expensify "$HOME/.local/bin/expensify"
export PATH="$HOME/.local/bin:$PATH"
expensify --version
```

Add the `export PATH=...` line to your shell startup file, such as `~/.zshrc` or `~/.bashrc`, if that directory isn't already on `PATH`. Installation doesn't require `sudo`.

These binaries aren't Apple Developer ID signed or notarized. If macOS blocks execution, verify the source and checksum before deciding whether to allow the executable through **System Settings > Privacy & Security**. The [macOS security guide](https://support.apple.com/guide/mac-help/open-a-mac-app-from-an-unknown-developer-mh40616/mac) explains this process.

## Windows

Run these commands in PowerShell, replacing the version with a published tag. Use `arm64` instead of `amd64` on Windows ARM devices. `v0.1.0` is an example version.

```powershell
New-Item -ItemType Directory -Force expensify-download | Out-Null
Set-Location expensify-download

$version = 'v0.1.0'
$arch = 'amd64'
$archive = "expensify_${version}_windows_${arch}.zip"
$base = "https://github.com/magarcia/expensify-cli/releases/download/$version"

Invoke-WebRequest "$base/$archive" -OutFile $archive
Invoke-WebRequest "$base/checksums.txt" -OutFile checksums.txt

$line = Get-Content .\checksums.txt | Where-Object { ($_ -split '\s+', 2)[1] -eq $archive }
if (-not $line) { throw 'The checksum list does not contain this archive.' }
$expected = ($line -split '\s+')[0]
$actual = (Get-FileHash -Algorithm SHA256 -Path $archive).Hash
if ($actual -ne $expected) { throw 'Checksum verification failed.' }

Expand-Archive -Path $archive -DestinationPath unpacked -Force
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\expensify'
New-Item -ItemType Directory -Force $installDir | Out-Null
Copy-Item .\unpacked\expensify.exe $installDir
$env:Path = "$installDir;$env:Path"
expensify --version
```

To use the command in future terminals, add `%LOCALAPPDATA%\Programs\expensify` to your **user** `Path` in **Edit environment variables for your account**. Reopen the terminal afterward. You don't need administrator access.

Windows binaries aren't Authenticode signed. A checksum detects a damaged or mismatched download; verify the GitHub release source before allowing an executable through Windows security prompts.

## Runtime requirements

The binaries use the operating system support of their Go compiler. Go 1.26 requires macOS 12 or newer, Windows 10 / Windows Server 2016 or newer, and Linux kernel 3.2 or newer. The [Go minimum requirements](https://go.dev/wiki/MinimumRequirements) describe platform details. Your browser and keyring service may require a newer OS.

Automatic login supports Chrome, Chromium, Microsoft Edge, and Brave. It creates a separate temporary profile and waits for you to sign in. Safari and Firefox aren't supported for automatic credential capture. Manual prompts, private files, stdin, and environment credentials also work; see [authentication](authentication.md).

OS keyring storage uses:

| Platform | Service |
| --- | --- |
| macOS | Keychain through `/usr/bin/security` |
| Linux | Secret Service over a user D-Bus session, with a usable login collection |
| Windows | Windows Credential Manager |

The default storage mode falls back to a private plaintext file if the keyring is unavailable, including on headless Linux hosts. Set `EXPENSIFY_CREDENTIAL_STORE=keyring` to require a keyring, or inject credentials from a secret manager for unattended use. See [credential storage](authentication.md#local-storage).

## Build from source

Install [Go 1.26 or newer](https://go.dev/dl/), [Python 3](https://www.python.org/downloads/), and [casey/just](https://github.com/casey/just#installation). The repository uses `just` as its build entry point.

```sh
git clone https://github.com/magarcia/expensify-cli.git
cd expensify-cli
just build
./bin/expensify --version
just install
```

The clone command requires the repository to have been published. Before publication, run the build commands in the local checkout.

`just install` installs to `GOBIN` when set, otherwise to the first `GOPATH` entry's `bin` directory. Inspect these values with `go env GOBIN GOPATH`, and add the chosen directory to `PATH`. On Windows, use `bin\expensify.exe` to run the local build. `just` recipes use Bash; Windows contributors need a Bash installation such as Git for Windows.

For a public module, the shorter Go-only installation also works:

```sh
go install github.com/magarcia/expensify-cli/cmd/expensify@latest
```

Go-only installation doesn't set the release metadata injected by `just build`, so `--version` may report `dev`. Use a release archive or the repository build when you need the embedded version, commit, and build timestamp.

## Agent skill

The [Expensify skill](../skills/expensify/SKILL.md) teaches agents to discover commands through CLI help and use `--no-input --output llm` for labeled records. Install the CLI first and ensure `expensify` is on your `PATH`.

Choose one installer. These examples install the skill for Codex across projects; replace `codex` with another supported agent, such as `claude-code` or `github-copilot`.

### GitHub CLI

Use a recent [GitHub CLI with `gh skill`](https://cli.github.com/manual/gh_skill), which is currently a preview feature:

```sh
gh skill install magarcia/expensify-cli expensify --agent codex --scope user
```

For the current project only, use `--scope project`. See [`gh skill install`](https://cli.github.com/manual/gh_skill_install) for supported agents, scopes, and version pinning.

### Vercel skills

With Node.js and npm installed, use [Vercel's skills CLI](https://github.com/vercel-labs/skills):

```sh
npx skills add magarcia/expensify-cli --skill expensify --agent codex --global
```

Omit `--global` to install into the current project. The `--skill expensify` option selects this skill by name.

### Local checkout

Remote installation requires the repository to be published on GitHub. From a local source checkout, or an extracted release archive containing `skills/expensify/SKILL.md`, use:

```sh
gh skill install . expensify --from-local --agent codex --scope user
```

The skill uses the installed CLI's help as its command reference. For agent commands, `--output llm` writes `Record N` sections and labeled fields to stdout, with pagination metadata for paginated results. Check the exit code and stderr for failures.

## Shell completions

Each release archive contains generated scripts in `completions/`. You can also generate them from the installed binary:

```sh
mkdir -p "$HOME/.local/share/bash-completion/completions"
expensify completion bash > "$HOME/.local/share/bash-completion/completions/expensify"

mkdir -p "$HOME/.zfunc"
expensify completion zsh > "$HOME/.zfunc/_expensify"

mkdir -p "$HOME/.config/fish/completions"
expensify completion fish > "$HOME/.config/fish/completions/expensify.fish"
```

Use the command for your shell. Bash needs `bash-completion` configured to load user completions. For zsh, add `fpath=("$HOME/.zfunc" $fpath)` before your existing `compinit` call; if you don't have one, add `autoload -Uz compinit; compinit` afterward. Fish loads its user completion directory automatically.

For PowerShell, create a script and dot-source it from your profile:

```powershell
$completionDir = Split-Path -Parent $PROFILE
New-Item -ItemType Directory -Force $completionDir | Out-Null
$completionScript = Join-Path $completionDir 'expensify-completion.ps1'
expensify completion powershell | Set-Content -Encoding UTF8 $completionScript
```

Add `. "$PSScriptRoot/expensify-completion.ps1"` to `$PROFILE`, creating the profile if necessary.

## Upgrade or uninstall

To upgrade, download and verify a newer release, then replace the executable in the same location. Credentials and settings stay in your existing OS keyring and configuration directory.

To remove saved credentials, run `expensify auth logout` before deleting the binary. Then remove the installed executable, completion scripts, and any `PATH` entries you added. Logout clears local credentials; it doesn't revoke the device session at Expensify. Remove the configuration directory separately if you also want to discard nonsecret settings.
