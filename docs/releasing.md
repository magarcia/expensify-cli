# Releasing

GitHub Actions builds and publishes a release when a maintainer pushes a version tag. The workflow runs native checks on macOS, Linux, and Windows, then cross-compiles six binary archives, verifies their contents and checksums, and creates a GitHub release with generated notes.

CI and release builds use the latest Go 1.26 patch; `go.mod` records the minimum supported Go version.

## First publication

Create an empty public repository at `github.com/magarcia/expensify-cli`. The Go module path and documentation use this name. If you choose another owner or name, update `go.mod`, Go imports, repository links, and workflow repository references before publishing.

For a local directory that hasn't been initialized as a Git repository:

```sh
git init -b main
git add .github .gitattributes .gitignore \
  README.md CONTRIBUTING.md SECURITY.md LICENSE THIRD_PARTY_NOTICES \
  go.mod go.sum justfile cmd internal integration scripts docs licenses skills
git diff --cached --stat
```

Review the staged files before committing. Credentials, browser profiles, receipts, generated binaries, and release archives belong outside the repository. The checked-in ignore rules exclude common generated and credential files; they don't replace this review.

After reviewing and running `just check`:

```sh
git commit -m 'Prepare the Expensify CLI for open source'
git remote add origin https://github.com/magarcia/expensify-cli.git
git push -u origin main
```

For an existing Git repository, inspect `git status` and `git remote -v`, then use its existing history and remote rather than initializing or replacing them.

Configure the following in GitHub repository settings. Checked-in files don't change these settings:

1. Set `main` as the default branch and enable GitHub Actions. Allow the official actions referenced by the workflows.
2. After the first successful CI run, protect `main` with pull requests and required checks. The workflow exposes `Check (ubuntu-latest)`, `Check (macos-latest)`, `Check (windows-latest)`, and `Check release archives`.
3. Enable Dependabot alerts and private vulnerability reporting. The [security policy](../SECURITY.md) explains how to report a vulnerability when private reporting is available or disabled.
4. Consider a tag ruleset for `v*` so only release maintainers can create version tags. Don't move or reuse a published tag.

Release automation uses the repository's `GITHUB_TOKEN`; it doesn't need an uploaded personal access token or Expensify credentials. Ordinary CI has read access. The release job requests content write access to publish assets and OIDC / attestation permissions for provenance. Organization policies must allow those workflow permissions.

## Prepare a version

Use tags of the form `vMAJOR.MINOR.PATCH`, for example `v0.1.0`. A tag such as `v0.2.0-rc.1` creates a prerelease. Examples in this guide don't imply that those versions already exist.

Before tagging:

```sh
git switch main
git pull --ff-only
git status --short
just check
just test-race
just release-snapshot
```

Start from a clean tree, wait for `main`'s GitHub checks to pass, and review user-facing behavior and documentation. `just check` includes formatting, `go vet`, tests, packaging tests, dependency notices, and a native build. `just release-snapshot` builds all six targets and validates the resulting archives and SHA-256 list.

Tests use fixtures and local subprocesses; they don't log in to a live Expensify account. For API or authentication changes, verify the installed build with your own account using `auth status --check` and a read command. Never put account credentials in CI.

To inspect archives with the intended release version locally:

```sh
just clean
VERSION=v0.1.0 just release
just release-verify
```

`just clean` removes generated binaries, `dist/`, and coverage output. Start with an empty release output directory so snapshots and old versions don't mix. You can use a separate directory with `VERSION=v0.1.0 ./scripts/build release --output-dir /path/to/release-output`.

## Publish

Create an annotated tag on the reviewed commit and push that tag:

```sh
git tag -a v0.1.0 -m 'Release v0.1.0'
git push origin v0.1.0
```

Pushing the tag authorizes automatic publication. The release workflow runs checks for the tagged source; a green result on another commit doesn't substitute for them.

The workflow publishes stable tags as normal releases and prerelease tags as prereleases. Prereleases don't replace the latest stable release. GitHub generates the release notes using the checked-in release-note configuration. Review the published notes and add installation, compatibility, or behavior details when generated pull request titles need context. See [GitHub's release documentation](https://docs.github.com/en/repositories/releasing-projects-on-github/managing-releases-in-a-repository).

## Release contents

| Platform | Architectures | Format |
| --- | --- | --- |
| macOS (`darwin`) | `amd64`, `arm64` | `.tar.gz` |
| Linux | `amd64`, `arm64` | `.tar.gz` |
| Windows | `amd64`, `arm64` | `.zip` |

For tag `v0.1.0`, assets use names such as `expensify_v0.1.0_linux_arm64.tar.gz`. The release also includes `checksums.txt` with SHA-256 hashes for the six archives.

Each archive includes the executable, README, license and dependency notices, documentation, generated shell completions, and `release.json` with its version, commit, build date, OS, and architecture. Build metadata comes from the version tag, commit, and commit timestamp. Release builds disable CGO and pin CPU requirements to `GOAMD64=v1` and `GOARM64=v8.0`.

The release workflow creates GitHub artifact attestations for public repositories. With a recent [GitHub CLI](https://cli.github.com/manual/gh_attestation_verify), verify a downloaded archive's provenance:

```sh
gh attestation verify expensify_v0.1.0_linux_arm64.tar.gz \
  --repo magarcia/expensify-cli
```

SHA-256 verification checks that the downloaded file matches the release list. Attestation verification checks its association with the repository's build workflow. Neither is Apple notarization or Windows Authenticode signing. See [installation](installation.md) for platform installation and checksum commands.

## Failures and corrections

Inspect the failed job in the repository's **Actions** tab. Fix source failures in a new commit and use a new version tag. If a job failed because of a temporary service or runner problem, rerun it from Actions for the same tag.

Don't overwrite a published version or replace its binaries. Publish a patch version for a fix. If a release has a security issue, follow [SECURITY.md](../SECURITY.md) and explain the affected versions in the release notes.

GitHub-hosted runners exercise the native macOS, Linux, and Windows builds used by CI. Cross-compiling an ARM64 archive proves that it builds and packages correctly; it doesn't replace native execution on every target architecture. Browser sign-in and OS keyring behavior still need platform testing outside unattended CI.
