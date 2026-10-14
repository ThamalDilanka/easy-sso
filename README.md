# easy-sso

**Your AWS accounts, roles, and connections. One terminal workspace.**

Sign in to AWS, choose a role, and open an EC2 shell or PostgreSQL session from a keyboard-and-mouse terminal app. Save connections, see which identity each session uses, and manage shells and database tunnels in one place.

![easy-sso terminal workspace showing the account and profile sidebar, active credentials, saved EC2 connections, and connection actions](docs/images/terminal-ui.png)

- Switch between configured AWS accounts and roles, with identity and credential expiry visible.
- Reconnect to saved EC2 instances through AWS Systems Manager Session Manager.
- Open database tunnels and PostgreSQL IAM terminals with hostname-verified TLS.
- Inspect and stop individual sessions or tunnels, and export connection bookmarks without credentials.

Built on the official **AWS CLI v2** and **Session Manager plugin**, with no background credential renewal or telemetry. You use your own AWS profiles and permissions; easy-sso does not grant access or configure your AWS resources.

[Motivation](#motivation) · [Install](#install) · [First run](#first-run) · [Credential security](#credential-security) · [Updates](#updates) · [Usage guide](docs/USAGE.md)

**[Try the offline browser demo](https://thamaldilanka.github.io/easy-sso/)** — it
opens directly in your browser. You can also download the single HTML file,
[`docs/demo/easy-sso-demo.html`](docs/demo/easy-sso-demo.html), and open it locally;
from a checkout, run `make demo` or double-click that file.

The [demo guide](docs/demo/README.md) covers its interactive, fictional workflows.
Everything runs inside that one file: no server, installation, credentials, or
network requests. Changes last only until reset/reload.

## Motivation

easy-sso grew out of two recurring problems: rebuilding connection commands and keeping the access available on a workstation aligned with the task at hand. Giving coding agents access to that workstation makes the second problem more important.

AWS Well-Architected guidance recommends Session Manager for administrative EC2 access. It avoids opening inbound SSH/RDP ports and managing SSH keys. This is the basis for easy-sso's EC2 connection workflow. See [AWS access guidance](https://docs.aws.amazon.com/wellarchitected/latest/framework/sec_incident_response_pre_provision_access.html) and the [Session Manager overview](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager.html).

> **Connection bookmarks**
>
> Finding an instance in the console, copying its ID, and rebuilding commands led to separate notes and shell snippets. AWS's [CLI connection workflow](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html) needs a target ID; database forwarding also needs a relay, endpoint, and ports. Console lookup is optional.
>
> easy-sso saves named bookmarks and builds repeat connection commands. You supply and maintain destinations. Database access still requires network reachability, authentication, and appropriate TLS configuration.

> **Explicit role activation**
>
> ReadOnly and Admin profiles can share a valid SSO session. Selecting ReadOnly does not prevent obtaining credentials for an already-assigned Admin role through another profile. AWS documents [SSO session reuse](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sso.html) and how [`--profile` overrides `AWS_PROFILE`](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html#cli-configure-files-using-profiles).
>
> easy-sso verifies selected role credentials and runs `aws sso logout` before publishing managed aliases. AWS's [logout operation](https://docs.aws.amazon.com/cli/latest/reference/sso/logout.html) clears cached SSO tokens and SSO role credentials across all profiles. Renewal is explicit; running connections retain their original credential snapshot.

> **Agent credential exposure**
>
> The [Agent Toolkit for AWS](https://docs.aws.amazon.com/agent-toolkit/latest/userguide/what-is-agent-toolkit.html) supports [profile allowlists and single-role OAuth sessions](https://docs.aws.amazon.com/agent-toolkit/latest/userguide/multi-account-access.html). Our concern is an agent that also has local shell and credential-file access: the CLI behavior above implies that a default profile alone cannot constrain every credential it can use.
>
> easy-sso clears cached SSO authorization after activation, reducing the access left available to local tools.

easy-sso reduces cached SSO access left available after activation, but **does not sandbox agents or enforce exclusive access to one role**. Other credentials, including previously activated aliases, can remain available; copied credentials and existing sessions are not revoked. Enforcing an agent's access requires least-privilege IAM permissions and an execution environment that exposes only the intended credentials. See [credential security](#credential-security) for the implementation and limits.

## Install

### 1. Install the prerequisites

Install [AWS CLI v2](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) and the [Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html). Install `psql` as well if you want PostgreSQL IAM terminals on Linux, WSL, or macOS.

For WSL, install these tools inside your Linux distribution. Native Windows uses Windows Terminal for external EC2 shell windows and supports database tunnels; embedded PostgreSQL terminals are available on Linux, WSL, and macOS.

You do not need Go, Make, or a source checkout to install a release.

### 2. Download version 1.0.0

Open the [v1.0.0 release](https://github.com/ThamalDilanka/easy-sso/releases/tag/v1.0.0). Download **one archive** from the table and **`SHA256SUMS`** from the same release into the same folder.

| Your system | Archive |
| --- | --- |
| Linux / WSL, Intel or AMD 64-bit (`x86_64`) | `easy-sso-1.0.0-linux-amd64.tar.gz` |
| Linux / WSL, ARM 64-bit (`aarch64` / `arm64`) | `easy-sso-1.0.0-linux-arm64.tar.gz` |
| macOS, Apple Silicon | `easy-sso-1.0.0-darwin-arm64.tar.gz` |
| macOS, Intel | `easy-sso-1.0.0-darwin-amd64.tar.gz` |
| Windows, Intel or AMD 64-bit | `easy-sso-1.0.0-windows-amd64.zip` |

On Linux, WSL, or macOS, `uname -m` shows the architecture of your current environment. Linux amd64 has been tested; macOS and Windows packages are cross-built with native qualification pending. See [platform status](#platform-status) before testing.

If you already use the optional GitHub CLI, you can download instead with the following example. Change the archive name to match your system:

```sh
gh auth login --hostname github.com
gh release download v1.0.0 --repo ThamalDilanka/easy-sso \
  --pattern 'easy-sso-1.0.0-linux-amd64.tar.gz' --pattern 'SHA256SUMS'
```

GitHub CLI is only a download convenience, not an AWS or easy-sso prerequisite. Hosted Homebrew and Scoop installation channels are not available yet.

### 3. Verify and install

#### Linux, WSL, and macOS

Open a terminal in the download folder. Set `archive` to the exact file you downloaded:

```sh
archive=easy-sso-1.0.0-linux-amd64.tar.gz

# Linux / WSL:
sha256sum "$archive"

# macOS (use this instead of sha256sum):
shasum -a 256 "$archive"

# Show the expected checksum for this archive:
grep -F "  $archive" SHA256SUMS
```

Compare the full SHA-256 value with the matching entry in `SHA256SUMS`. **Continue only if they match.** Then extract the complete archive into a dedicated installation directory:

```sh
mkdir -p "$HOME/.local/lib/easy-sso"
tar -xzf "$archive" -C "$HOME/.local/lib/easy-sso"
chmod 755 "$HOME/.local/lib/easy-sso/easysso"
export PATH="$HOME/.local/lib/easy-sso:$PATH"
easysso version
```

Add the same `export PATH="$HOME/.local/lib/easy-sso:$PATH"` line to your shell configuration (`~/.bashrc` for Bash or `~/.zshrc` for Zsh) so new terminals can find the command. Keep this installation directory writable by your user for updates; do not run the updater with `sudo`.

#### Windows (PowerShell)

Open PowerShell in the download folder and compare the hash with the archive's entry in `SHA256SUMS`:

```powershell
$archive = 'easy-sso-1.0.0-windows-amd64.zip'
Get-FileHash $archive -Algorithm SHA256
Select-String -Path .\SHA256SUMS -SimpleMatch $archive
```

**Continue only if the full hashes match** (letter case does not matter). Extract and add the directory to the current terminal's PATH:

```powershell
$installDir = Join-Path $env:LOCALAPPDATA 'easy-sso'
Expand-Archive -Path $archive -DestinationPath $installDir
$env:Path = "$installDir;$env:Path"
easysso version
```

For future terminals, open **Edit environment variables for your account**, edit your user **Path**, and add `%LOCALAPPDATA%\easy-sso`. Open a new terminal afterward.

On every platform, keep **`easy-sso.install.json` beside the executable**, and retain the bundled license and documentation. Extracting the whole archive as above preserves these files. These steps are for a first installation; see [updates](#updates) before replacing an existing copy. Checksums detect corruption but are not an independent publisher signature.

## First run

If you have not configured an AWS SSO profile yet, run the AWS CLI setup and enter the IAM Identity Center details provided by your organization:

```sh
aws configure sso
```

Then check your local setup and start the app:

```sh
easysso version
easysso diagnostics
easysso
```

Diagnostics checks prerequisites and profile definitions without signing in to AWS. Its output contains account IDs, roles, and local paths; redact it before sharing.

1. Select your profile and press **Ctrl+A** to sign in. Open or copy the displayed browser link and complete the device-code flow.
2. Choose **EC2** or **Databases**, then press **n** to enter a connection. Supply the instance ID or database endpoint and relay details from your team, review the destination, and connect. Successful connections become bookmarks.
3. Press **Enter** on a saved connection to reconnect. On Linux, WSL, and macOS, use **Shift+Tab** to cycle through terminal tabs and **Ctrl+]** to return to the workspace.
4. Open **Sessions** or **Tunnels** to inspect or stop a run. Press **?** for the shortcut reference.

Database connections require an existing SSM relay and network access to the endpoint. PostgreSQL IAM login also requires an IAM-enabled database, an authorized database user, and the appropriate AWS role permissions. See the [usage guide](docs/USAGE.md#postgresql-iam-terminals) for setup details and tunnel-only access with your own database client.

Use **Ctrl+T** to switch light/dark themes. Mouse navigation, ASCII mode, and `NO_COLOR` are supported. The [full usage guide](docs/USAGE.md) covers credential renewal, bookmark import/export, keyboard controls, and session recovery.

## Credential security

![Credential flow: obtain selected temporary role credentials, verify their identity, clear AWS CLI SSO authorization, then save credentials only after cleanup succeeds; running connections keep their captured identity](docs/images/credential-security.png)

When you activate a profile, easy-sso obtains the selected role's temporary credentials, verifies the account and role, and runs `aws sso logout` **before saving those credentials** to managed aliases in your AWS shared credentials file. If cleanup fails, it does not publish the new credentials. Renew access explicitly when needed.

**Cleanup affects all cached SSO sessions in the current AWS CLI environment.** Other tools using those caches may need you to sign in again. The diagram's ReadOnly role is an example; your selected role's actual IAM permissions still apply, including any permission to assume another role. easy-sso does not remove those permissions.

Each running connection keeps the identity and credential snapshot it started with. Selecting another profile does not retarget an open shell or tunnel. SSO cache cleanup and local credential removal do not revoke already-issued credential copies or end established sessions.

See [security and data handling](SECURITY.md) for storage, cleanup, and session guarantees and limits.

## Updates

Updates are explicit; easy-sso does not check in the background. Releases are public, so no GitHub token is required. If you hit GitHub's anonymous API rate limit, you can optionally set **`EASY_SSO_GITHUB_TOKEN`** to a GitHub token; it is sent only to GitHub's API. The app does not automatically use GitHub CLI credentials.

Check for a newer compatible release:

```sh
easysso update --check
```

For a direct Linux/WSL/macOS installation, close all easy-sso apps and helper windows using that installation, then install the available update:

```sh
easysso update
```

You can also download a release through your browser for a manual upgrade.

**Windows upgrades are manual:** close all easy-sso apps and helper windows, download and verify the new archive, and replace the executable and bundled files. Retain the previous archive for recovery. Updates preserve AWS configuration, credentials, bookmarks, and application settings. See [installation, updates, and uninstall](docs/DISTRIBUTION.md) for details.

## Platform status

| Platform | Terminal experience | Validation |
| --- | --- | --- |
| Linux / WSL | Embedded EC2 and PostgreSQL tabs; optional external EC2 windows | Linux amd64 tested, including real PTYs with simulated AWS; WSL interop needs native validation |
| macOS | Embedded tabs; optional Terminal.app windows | Cross-built; native validation pending |
| Windows | External EC2 windows through Windows Terminal; database tunnels | Cross-built; native validation pending |

Live AWS/RDS qualification is still pending. Automated tests use simulated AWS responses and do not establish production readiness. See [validation](docs/VALIDATION.md) for the checks and remaining limitations.

## Contributing

For source builds and tests, see [Contributing](CONTRIBUTING.md). The [architecture guide](docs/ARCHITECTURE.md) describes the implementation.

Licensed under the [MIT License](LICENSE).
