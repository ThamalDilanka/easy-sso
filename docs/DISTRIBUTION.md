# Installation, updates, and release operations

Releases are published on GitHub as `v<version>` tags; downloading needs no account or token. The release workflow creates **drafts only**, which the maintainer reviews before publication. Cross-built macOS/Windows packages still require native qualification. AWS CLI v2, Session Manager plugin, and optional `psql` must be installed separately.

## Direct installation

Choose a published tag and the archive for your OS/CPU on [Releases](https://github.com/ThamalDilanka/easy-sso/releases). Names are `easy-sso-<version>-<os>-<arch>.tar.gz`, or `.zip` for Windows. Supported build targets are Linux amd64/arm64, macOS darwin amd64/arm64, and Windows amd64. WSL uses Linux packages.

Download the archive and `SHA256SUMS` from the same release. Compare its SHA-256 with its exact manifest entry before extraction: `sha256sum <archive>` on Linux, `shasum -a 256 <archive>` on macOS, or `Get-FileHash <archive> -Algorithm SHA256` in PowerShell. Do not ignore a mismatch. Checksums detect corruption; a checksum hosted beside its archive is not an independent publisher signature. Release signatures and attestations are not configured.

On Linux/macOS, extract to a temporary directory, then copy **both** `easysso` and `easy-sso.install.json` into a dedicated directory such as `$HOME/.local/lib/easy-sso`. Set the binary executable (`chmod 755`). Add that directory to PATH, or create a symlink from `$HOME/.local/bin/easysso` to it. Keep the bundled MIT license and documentation alongside the binary. The installation directory must be writable by your user for self-updates. Do not run the updater with sudo. The marker records the installation channel and canonical repository; a lone copied executable intentionally has unknown provenance.

On Windows, extract into a dedicated user directory and add it to PATH. Keep `easy-sso.install.json` beside the executable. Close all easy-sso apps and helper windows before manually replacing an existing installation. Windows self-replacement is intentionally unavailable: the running executable can be locked, and native replacement has not been qualified. A failed manual replacement must be resolved before reopening the app; retain the previous archive for recovery.

For manual upgrades on any platform, close all apps and helpers, verify the new archive, and replace the binary and bundled files. Do not overlay a package-manager installation manually. Older versions without the executable lock must be closed before upgrading.

## Explicit updates

```sh
easysso version
easysso update --check
easysso update
easysso update --check --prerelease
```

Checking lists the current and highest compatible semantic version and installation-specific guidance. It skips drafts, development versions, and releases without the exact platform archive and checksum manifest. Stable releases are the default; `--prerelease` opts into prereleases for checking or installation. It never downgrades. Development, Go pseudo-version, or unknown builds must be installed explicitly rather than treated as outdated stable builds.

`update` itself is the deliberate installation action; there is no second confirmation. Direct Unix installations are downloaded, checksum-verified, inspected for exact archive contents, OS/CPU and embedded version, staged beside the executable, then replaced by atomic rename with the original executable permissions. Verification and replacement failures leave the existing executable intact. The temporary file is removed on normal failure; an abruptly killed updater can leave `.easy-sso-update-*` files beside the executable, which can be removed after confirming no updater is active.

Apps and independent helpers hold shared locks on the installed Unix executable, across custom state directories and users. Replacement requires an exclusive lock, so close those apps/helpers and retry if the updater reports activity. Other copies are separate installations. An update never restarts the UI, changes session identities, terminates sessions, replaces AWS helper tools, or reads/modifies AWS files, settings, bookmarks, or runtime records. There are no startup/background checks, telemetry, or UI networking.

Homebrew, Scoop, and WinGet locations/metadata receive manager guidance instead of replacement. Go-installed binaries receive a versioned `go install` command. Unknown provenance receives reinstall guidance. Candidate channel manifests override the archive's direct-install marker with their manager channel.

All requests use bounded sizes, timeouts, HTTPS, and the fixed canonical GitHub repository. A check does not write files or authenticate to AWS. Offline, authorization, no-release, and rate-limit conditions are reported explicitly. Public releases need no token. Optionally, set `EASY_SSO_GITHUB_TOKEN` to a GitHub token to raise GitHub's anonymous API rate limit. Tokens are sent only to GitHub's API and stripped from redirects; errors omit response bodies and token-bearing URLs. There is no automatic use of `gh`, Git, or AWS credentials.

## Source and package managers

Install the latest tagged release from source, or replace `latest` with a specific tag such as `v1.0.0` to pin one:

```sh
go install github.com/ThamalDilanka/easy-sso/cmd/easysso@latest
```

`version` uses Go module build metadata when release linker flags are absent. Go installations receive a versioned `go install` command from `easysso update --check` instead of in-place replacement; `EASY_SSO_GITHUB_TOKEN` only affects the updater's GitHub requests.

`make package VERSION=<version> COMMIT=<commit>` generates candidate Homebrew formula and Scoop manifests in `bin/channels/`, using the same archive names and checksums as the release. They are **not hosted channels yet**. Once a tap or bucket is published, use `brew upgrade easy-sso` or `scoop update easy-sso`. Each release requires regenerating/updating the manifests. WinGet submission is deferred until native Windows qualification.

## Uninstall

Close all apps and helpers first. For direct installations, remove your executable/symlink and dedicated installation directory. For Go installations, remove the binary from GOBIN (or GOPATH/bin). For package managers, use `brew uninstall easy-sso` or `scoop uninstall easy-sso`. Uninstalling does not clean AWS credentials or terminate remote sessions: use easy-sso's explicit session and credential cleanup before uninstalling if needed. Local settings and bookmarks remain in the paths described in [Usage](USAGE.md); delete them separately only if intended. Never delete the entire `.aws` directory as part of uninstalling this app.

## Maintainer release workflow

Keep one pipeline: `tools/package.py` builds all targets from `internal/releases/contract.json`, packages only its allowlisted documents and installation metadata, writes five versioned archives to `bin/release/`, and creates `SHA256SUMS` covering exactly those archives. The manifest does not hash itself. Loose binaries, channel candidates, state, credentials and backups are excluded. Tar metadata, gzip timestamps, ZIP timestamps, order, and modes are fixed; identical source/toolchain/version/commit inputs produce identical archives.

The release workflow accepts a pushed `v*` tag or manual dispatch with an **existing** tag. It validates complete SemVer, verifies the tag is on main, runs tests/checks/history scanning, builds and validates packages, then creates one reviewable draft with only five archives plus `SHA256SUMS`. It refuses to modify an existing release. Ordinary CI has read-only contents permission. Only the final draft job has contents write permission. Actions are pinned to commits; publication uses the Python standard library and GitHub REST rather than an unpinned release utility.

A local dry run (does not create tags or contact GitHub):

```sh
make test
make check
make package VERSION=0.1.0-rc.1 COMMIT=unreleased
EASY_SSO_RELEASE_DIR="$PWD/bin/release" go test ./internal/updater -run TestPackagedRelease -count=1
python3 tools/check-public.py --history
```

The local version above is a dry-run fixture, separate from `v1.0.0`. Before publishing a draft, review the actual draft assets, commit/version attribution and MIT license; qualify native platforms and live AWS behavior from [Validation](VALIDATION.md).

Primary references: [GitHub release API](https://docs.github.com/en/rest/releases/releases), [release assets](https://docs.github.com/en/rest/releases/assets), [Go versioned installation](https://go.dev/ref/mod#go-install), [Homebrew formula DSL](https://docs.brew.sh/Formula-Cookbook), [Scoop manifests](https://github.com/ScoopInstaller/Scoop/wiki/App-Manifests).
