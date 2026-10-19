# Using easy-sso

## Installation and updates

See [Distribution](DISTRIBUTION.md) for direct installation, checksum verification, package-manager candidates, versioned Go installation, and uninstall instructions. Use `easysso update --check` to check deliberately and `easysso update` to install a newer compatible release from a recorded direct Unix installation. Add `--prerelease` to opt into prereleases. Close apps/helpers before replacing their executable. Updates preserve AWS files and application state. Native Windows uses manual replacement; no automatic checks run in the workspace.

## Build and run

Use Go **1.27.2 or newer**, with exact dependency versions and checksums in `go.mod` and `go.sum`.

```sh
make build
./bin/easysso version
./bin/easysso diagnostics
./bin/easysso
```

Install AWS CLI v2, the official Session Manager plugin, separately (Windows native execution also needs a supported terminal). Configure your SSO sources with the official AWS CLI before running the app. Missing prerequisites leave bookmark browsing and backup available and disable dependent actions. Diagnostics checks executable versions and reads profile definitions; it does not authenticate or call AWS services.

```sh
./bin/easysso --state-dir /private/path/easy-sso
./bin/easysso --aws-config /path/config --credentials-file /private/path/credentials
./bin/easysso --aws-cli /path/to/aws
```

The Go module is `github.com/ThamalDilanka/easy-sso`. The command and its source directory are `easysso` (`cmd/easysso`); the product name `easy-sso` is used for the SSM reason prefix, workspace title, configuration directory, and `EASY_SSO_*` environment variables. Defaults honor `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, the current runtime's home, and `os.UserConfigDir()/easy-sso`. Explicit path overrides do not move AWS CLI SSO caches. Existing application/credential directories on Unix must be owned by the current user and private (0700); exported credential files must be 0600. Unsafe paths and symlinks are rejected. Windows uses a protected DACL restricted to the current user; native execution remains a verification gate.

Profiles, account IDs, roles, source chains, SSO session names and login configuration come from the selected AWS config. The app rereads it every two seconds while idle; `Ctrl+R` requests an immediate reload. It preserves the selected profile by name, invalidates pending profile approvals when definitions change, and blocks unavailable profiles without inventing missing definitions. Existing sessions keep their frozen identities. Region suggestions come from the profile, inherited source, or configured SSO region. Account labels default to the account ID and change only through explicit saved user labels. EC2 instance IDs and database endpoints are entered as connection bookmarks because AWS profile configuration does not contain these resource targets.

WSL runs the Linux executable, CLI, plugin, home, and credentials. The terminal launcher pins the current distribution/user. It does not switch to Windows AWS credentials. Linux, WSL and macOS use an embedded PTY terminal, with no desktop terminal launcher required. Native Windows retains the external terminal adapter.

Sign-in uses the official CLI device-code flow (`--use-device-code --no-browser`). The app displays the sign-in URL and code without opening a browser automatically. Click the link or **Open browser (o)** to open your default browser (the Windows browser on WSL), or press **c** to copy the URL; the app confirms when copying succeeds. Complete sign-in in your preferred browser. Only temporary role credentials are published after identity verification and successful SSO cache cleanup; broad SSO tokens are cleared automatically, including after cancellation or failure. Initial AWS profile configuration is still required.

## Terminal appearance

The workspace has a persistent source/account/profile sidebar, identity header beside it, six compact views, subtle separators, and blue selection. Click account headings to expand or collapse them without changing identity; click a profile to select it. The selected account also expands automatically during keyboard navigation. Tab cycles Accounts → EC2 → Databases → Accounts. Keys 3–6 open Sessions, Tunnels, Credentials, and Backup directly; they are outside the Tab cycle. EC2 and Databases sit on the left of the view strip, with the other views aligned right; selected views have an underline. At 80–99 columns, the Accounts step opens the account picker; the identity header remains visible during review. The light theme is the default. Click the theme hint in the footer or press Ctrl+T to switch; the choice is saved with local settings. EASY_SSO_THEME=dark or light overrides the saved preference at startup. NO_COLOR and EASY_SSO_ASCII remain supported.

If your shell exports `NO_COLOR` (even an empty value), the application respects it. To enable colors for one run on Linux/macOS:

```sh
env -u NO_COLOR TERM=xterm-256color ./bin/easysso
env -u NO_COLOR TERM=xterm-256color EASY_SSO_THEME=light ./bin/easysso
```

Development screenshots and terminal recordings stay local under the ignored `docs/screenshots/` directory. External window focus remains adapter-dependent; see [release validation](VALIDATION.md) for live AWS qualification requirements.

## Use

1. Select a profile with `Tab` and the arrow keys. The header shows the exact destination account, role, partition and SSO source, including a role chain.
2. Select a profile and click **Log in (Ctrl+A)** on the right side of the account header, or press `Ctrl+A`. Plain `a` does not trigger login or logout. When the selected profile has active keys, this becomes **Log out (Ctrl+A)**; confirmation removes that profile’s managed keys while existing shells and tunnels remain open. Complete sign-in using the displayed link or copy it with `c`. The app keeps only that profile’s temporary role credentials and automatically clears AWS CLI SSO caches. `Shift+A` opens advanced selection of multiple roles and credential aliases. Renew explicitly from Credentials; no refreshable SSO authorization is retained by this tool.
3. Use `1` for EC2 or `2` for Databases. Resource tables have numbered rows, extra spacing and full-row selection highlights. In EC2 and Databases, selected connection details appear above the action buttons. Connect and EC2 New window come first, with Edit and Details aligned right; narrow layouts wrap these groups. New connection sits at the right of the section heading. Press `n` to enter a new connection, review full identity and regions, then confirm. New bookmarks are saved only after confirmed SSM/listener readiness; PostgreSQL IAM bookmarks additionally require an authenticated client prompt.
4. For databases, enter the remote endpoint/port, database region, relay EC2 instance and its SSM region, and local port. New connections default to `postgres-iam`: add the database name and database username, then choose **Connect terminal (Enter)**. The app opens or reuses an owned tunnel and signs into an embedded `psql` terminal using the selected role. Choose `tunnel` for transport only; existing bookmarks retain that behavior until edited. `Enter` connects directly from the saved list; `v` opens the full route and login details without connecting.
5. Use `3` for Sessions and `4` for Tunnels. Both list newest runs first; Sessions shows the latest 50 records and Tunnels keeps all records available. The selected run and bound profile stay above the table. `v` opens its full details at the top; `Esc` returns to the same list position. `Enter` focuses an existing terminal when available, otherwise opens the record details. `d` opens a targeted disconnect/stop review and `r` rechecks uncertain status. In EC2, **Connect terminal (Enter)** opens or focuses a named easy-sso tab alongside **[AWS] easy-sso**. While a new connection opens, the Connect terminal button shows an amber spinner, blocks repeat clicks, and keeps Cancel available. Click a tab or use **Shift+Tab** to switch; **Ctrl+]** returns to the AWS workspace with its account, view and server selection preserved. Each shell retains its screen and incomplete input independently of the selected profile. Each shell also keeps up to 5000 lines of earlier output: scroll it with the mouse wheel over the pane or **Alt+PgUp/PgDn**; the hint row shows how far back you are, and any key returns to live output. Inside a full-screen program such as `less` or `vim` the wheel and page keys go to the program instead. The tab strip has two rows of height; long strips scroll to keep the selected tab visible. Shell tabs show the tab strip, terminal area and keyboard shortcuts; failed startups also show a clear error banner. When the SSM client ends while a tab is open, for example after the Session Manager idle timeout or a dropped network link, the tab keeps its last screen under a **Session ended** banner, stops forwarding keys, and offers **Reconnect (r / Enter)**, which opens a replacement shell with the same frozen credentials and destination. Reconnect is refused once those credentials have expired; renew the role and connect again from EC2. If the replacement fails to start, its tab opens beside the ended one with the failure banner and the client's diagnostics. **Ctrl+W**, **Alt+W**, or the tab close button disconnects and closes the selected local shell. If local cleanup fails, its tab stays available with an error. Use shell `exit` or Sessions → Disconnect to end it. **New window (w)** explicitly starts an additional shell in a separate terminal window. **Details (v)** retains the full review and New shell action.
6. Use `5` for Credentials, with an aligned profile/account/status/remaining-time table and full details for the selected row. The **PROFILE** column is the exact name to pass as `--profile` to the AWS CLI and SDKs; the details repeat it as `Use: --profile <name>`, and `c` copies it to the clipboard (only the name, never keys). Expiry countdowns update every second using Bubbles timers; dates use readable local time. Use `r` to renew, `d` to remove, `u` to unpublish or republish, and `R` to retry incomplete SSO cleanup. **Unpublish** takes the selected role's entry out of the AWS credentials file, so the profile name has no credentials behind it (`--profile <name>` fails with a missing-credentials error; the name itself stays listed) while its keys stay only in this app's memory; **Republish** puts the same entry back without a new sign-in. Exiting the app drops held keys, so an unpublished role still shows as unpublished after a restart and must be renewed. Unpublishing is hygiene for the shared credentials file, not a security boundary: processes running as your user can still read this app's memory, and already-open shells and tunnels keep their frozen credentials. While a role is unpublished or removed, new connections and shell reconnects with it are refused. Removal deletes owned local material; it does not revoke credential copies or established runs.
7. Use `6` for Backup. `e` exports to a **new** file path. Existing files and reserved AWS/application paths are protected from overwrite. `i` validates an import, previews additions/conflicts, and preserves local records by default; replacement is explicit. Missing profile hints are marked as needing rebinding; select and review a valid matching account/role before connecting.
8. `q` or `Ctrl+C` opens exit review. Normal exit disconnects embedded shells and stops owned DB tunnels; external window shells remain independent. While a terminal tab is selected, keys including Ctrl+C go to the shell, except the reserved tab shortcuts above; use Ctrl+] before workspace shortcuts. Embedded shells belong to this app process and cannot survive its exit.

Mouse clicks work on view tabs, account/profile rows, saved resources, action buttons, fields, role checkboxes, and cleanup acknowledgements. The wheel scrolls the panel under the pointer, including a shell tab's retained output. Clicking a resource selects it; Enter connects the selected EC2 or database bookmark. Use v to open details separately. Mouse release and drag events never submit an action. All keyboard controls remain available.

`y` edits the selected account's local display label. Labels are never IAM identity. Use `NO_COLOR=1`, `EASY_SSO_ASCII=1`, and `EASY_SSO_THEME=light` for terminal preferences. `?` opens categorized Key / Action tables covering navigation, accounts, every view, forms, terminal tabs, sign-in, and exit. Use PgUp/PgDn to scroll and ?/Esc to close Help before using workspace shortcuts; `Ctrl+Left`/`Ctrl+Right` cycles through the six views (outside forms and reviews); `Tab` cycles Accounts, EC2, and Databases; form `Ctrl+U` clears a field.

Shift+Tab cycles forward through the top-level tabs and wraps back to AWS after the last shell. There is no previous-tab shortcut. In forms, Tab or Down moves to the next field and Up moves to the previous field.

## Session lifecycle

Independent helpers retain credentials in memory and advertise private, authenticated control endpoints. Sessions shows the latest 50 records, newest first, rather than only open terminal tabs. The limit applies to the displayed list; runtime history is retained. For the shown records, active, unverified, and history counts are separate, and each row identifies an open or unlisted local tab, history, or no local tab. Start times are fixed local dates/times; active rows are green and ended rows are gray. Runtime records currently have no automatic expiry. A restarted TUI preserves Ended/Failed shell history and its diagnostics; stale nonterminal helpers display Unverified. Recovery never kills a recovered PID. Lost transport is never reconnected automatically: a shell whose SSM client exits is marked Ended with the client's reason, and **Reconnect** starts a new session only on request. While it reconnects, the ended tab drops the reconnect prompt and its bottom row shows a `Reconnecting` spinner with the elapsed time, ignores repeat presses, and `Esc` cancels. Session Manager ends idle sessions after the account's idle timeout (20 minutes by default, configurable between 1 and 60 minutes), which the client reports as a timed-out or exiting session. A dropped link, such as sleep, a VPN change or a network switch under WSL2, surfaces instead as a broken-pipe write error on the next keystroke; that message stays visible in the tab's last screen under the banner. No keepalive input is ever sent. A recovered tunnel candidate needs independent process/AWS verification; no blind orphan kill occurs. `RemoteEnded` distinguishes confirmed remote termination from local process cleanup. Retain the runtime metadata for manual investigation when termination cannot be confirmed.

The cache cleanup boundary is the current official CLI environment. Already-issued role credentials, same-user copies, unrelated credential files, browser/IDE/SDK stores, and other Windows/WSL environments remain outside it. This application is not an OS isolation boundary and does not narrow IAM permissions.

Exit uses a standalone confirmation when local shells, owned tunnels, confirmed external shells, or saved managed credentials are present. Only relevant counts are shown. History and unverified external records do not count as open shells. With nothing to warn about, `q` runs normal cleanup and exits immediately. **Exit (Enter)** keeps saved credentials; **Clear creds & exit (c)** removes easy-sso managed credentials after session cleanup, and **Cancel (Esc)** returns to the workspace. Clearing uses ownership checks and does not revoke copied credentials or sign out independent external shells. Cleanup failures keep the confirmation open with an error.

Press `?` in the workspace for the contextual shortcut reference.

A rejected SSM shell startup is recorded as Failed. AccessDeniedException is shown as an access-denied explanation for ssm:StartSession on the instance and session document, naming the bookmark's document or `SSM-SessionManagerRunShell` by default; raw AWS diagnostics remain in the terminal and are not copied into runtime metadata. Closing a failed tab releases its local terminal without attempting to terminate an unconfirmed remote session.

Saved EC2 and database connections support **Edit (`e`)** from the list or connection review. Edit any connection field, then press `Ctrl+S` (or Enter on the final field) to save without connecting. `Esc` discards edits. Account ownership stays fixed, and open sessions retain their original route. The database endpoint is a hostname only, separate from the display name.

EC2 bookmarks have an optional **Session document** field. Leave it blank to use the account's Session Manager preferences, as before. Enter a document name such as `Example-ShellDocument`, or the full ARN of a document shared from another account, such as `arn:aws:ssm:us-east-1:111122223333:document/Example-ShellDocument`. An ARN must be in the bookmark's partition and region, because SSM documents are regional. Connect, New window, Reconnect and recovery pass it as `--document-name`. Documents that do not open a shell are refused: `AWS-StartPortForwardingSession`, `AWS-StartPortForwardingSessionToRemoteHost`, `AWS-StartSSHSession` and `AWS-StartNonInteractiveCommand`. easy-sso does not check the document with AWS first, so a misspelled or non-shell custom document fails when the session starts. The same instance with a different document is a separate bookmark. Reviews show the document, and import previews name it so an imported file cannot quietly change which document a shell runs. Releases before 1.2.0 reject a bookmarks file once any bookmark has a session document; files without one stay compatible.

## PostgreSQL IAM terminals

Install `psql` alongside the AWS CLI and Session Manager plugin. PostgreSQL terminals use app-owned PTYs on Linux, WSL, and macOS; native Windows currently supports tunnel-only database access. Select **Set up IAM (l)** on a tunnel-only bookmark, provide the **database name** and **database username**, then save with Ctrl+S and choose Connect terminal (Enter). The full editor (`e`) also exposes the authentication mode. Neither is inferred from the SSO role.

The database must already have IAM authentication enabled, the database user must have `rds_iam` and appropriate SQL grants, and the selected AWS role must allow `rds-db:connect` for that database resource and user. AWS read-only permissions do not determine SQL permissions. The app does not enable IAM authentication or change grants. Tunnel-only success explicitly says database login was not attempted. The Databases view shows the profile bound to an open tunnel, even after another profile is selected. SSM access-denied failures name the relay/session-document permission and do not imply database login succeeded. See [AWS PostgreSQL IAM prerequisites](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.Connecting.AWSCLI.PostgreSQL.html).

**Waiting for IAM setup?** Choose **Edit (e)**, set **Auth** to `tunnel`, and save with Ctrl+S. Enter then opens the tunnel for a database client using an existing database login. **Set up IAM (l)** only saves connection settings; an administrator must configure the RDS setting, database user/grants, and SSO role permission for the exact DB resource ID and username. An authentication rejection lists these checks because the PostgreSQL error does not identify which prerequisite is missing.

Connect terminal uses the selected role's frozen temporary credentials to generate a fresh token for the real RDS hostname, database port, and DB region. `psql` receives the token only through its child environment; bookmarks, command arguments, runtime metadata, and error messages contain no token. Client history is disabled and the token is redacted from rendered output. Tokens last 15 minutes for initial authentication; reconnect using Connect terminal to generate a new one. The app marks the run Connected only after `psql` emits its authenticated interactive prompt, and setup failures retain useful terminal output.

TLS uses `verify-full`, the real endpoint as `host`, and `127.0.0.1` as `hostaddr` with the local tunnel port. Leave **RDS CA file** blank to download and cache the official public AWS bundle (commercial/GovCloud partitions, refreshed after 24 hours), or supply an existing PEM bundle for offline access. China partitions require an explicit bundle. A custom path may need changing after bookmark import on another machine. See [libpq host/hostaddr](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNECT-HOSTADDR) and [AWS RDS CA bundles](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.SSL.html).

Repeated Connect focuses the existing authenticated client when route, database user/name, and frozen AWS identity match. Only a listener verified as owned by this workspace can be reused. Different identities and externally opened tunnels require another local port or explicit cleanup. Recovered registrations never transfer process ownership and do not reserve an otherwise free local port. One client attaches per tunnel. Closing the terminal or entering `\q` stops a tunnel created for it; a separately opened tunnel stays available. **Tunnels → Stop** and workspace exit stop both owned transport and its client. Remote termination failures remain explicitly unconfirmed.

Validation uses fake AWS calls and a real local `psql`/TLS protocol fixture. This does not establish live RDS IAM permissions or production login readiness.

After a restart, Tunnels automatically checks older records using their original managed profile. Closed sessions become stopped history. **Remote active** means AWS verified the saved session; **Stop (d)** terminates that exact session. **Needs login** names the original profile to renew; **Check failed** explains an identity, permission or network problem; **Port in use** means local listener cleanup still needs checking. Use **Recheck (r)** to retry immediately. Recovery does not reconnect, restore terminal screens or kill processes by saved PID.

## DocumentDB tunnels (DataGrip and other clients)

Amazon DocumentDB bookmarks open a tunnel with your SSO credentials only. Your DocumentDB user and password stay in DataGrip or your other MongoDB client: easy-sso never asks for, copies or stores them, and its copied strings never contain a DocumentDB user name or password. The CA file it handles is the public AWS certificate bundle, not a secret.

An endpoint ending in `.docdb.amazonaws.com`, `.docdb.amazonaws.com.cn` or `.docdb-elastic.amazonaws.com` is treated as DocumentDB, and reviews show **Engine** `DocumentDB`. In a new connection, leaving the endpoint field switches fields still at their defaults to DB port 27017, local port 37017 and Auth `tunnel` (`DocumentDB endpoint: ports set to 27017/37017, auth set to tunnel`). `postgres-iam` is refused for DocumentDB when saving, editing, connecting or importing (`PostgreSQL IAM login does not apply to DocumentDB; set Auth to tunnel`), and **Set up IAM (l)** explains that it is for PostgreSQL. DocumentDB bookmarks use the existing tunnel format; no new bookmark field is added.

**Prerequisites:**

- The relay EC2 instance runs in the cluster's VPC or in a peered VPC. AWS documents tunnel access from outside the VPC through such an instance ([connecting from outside a VPC](https://docs.aws.amazon.com/documentdb/latest/developerguide/connect-from-outside-a-vpc.html)).
- The cluster's security group allows inbound TCP on the cluster port (27017 by default; elastic clusters use only 27017) from the relay.
- The relay runs SSM Agent 3.1.1374.0 or later, which remote-host port forwarding requires ([Session Manager port forwarding](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-sessions-start.html)). Your role needs the same Session Manager permission on the relay as any database tunnel.
- Your administrator has created the DocumentDB users you log in with, for example one read-only and one read-write user. AWS role permissions do not decide what a DocumentDB user may read or write.
- Copy keys use the system clipboard: native Linux needs `xclip`, `xsel` or `wl-copy`, and WSL uses `clip.exe`, which needs WSL interop. macOS tunnels need `lsof` for the listener check.

**Open the tunnel.** Press `Enter` on the saved bookmark. The status reads `Tunnel open at 127.0.0.1:37017 · profile [<profile>]. Connect DataGrip or any MongoDB client with your DocumentDB user; c copies the URL, v shows steps.` The connection review and the run details (`v` in Tunnels) show a **DATAGRIP (or any MongoDB client)** block with the values below; the URL sits alone on its line so you can also select it by hand. `c` and `C` work in Databases, in the connection review, and on an open run in Sessions, Tunnels or run details. They use the open tunnel's port, otherwise the bookmark's local port. On a run that is no longer open they report `Tunnel not open; Enter on the bookmark reopens it.`

**Logins.** easy-sso does not log in to DocumentDB; the tunnel is transport only. Add one DataGrip data source per DocumentDB user, for example a read-only and a read-write one; both point at the same `127.0.0.1` port, so one open tunnel serves them (`One tunnel serves every user: one data source each`). Each client keeps its own saved password.

**CA bundle.** Leave **RDS CA file (blank: automatic)** empty and easy-sso downloads the public AWS certificate bundle for the commercial and GovCloud partitions. It is cached for 24 hours and checked when the tunnel opens, when you open its details (`v`) and when you press `C`:

| Platform | Cached CA file |
| --- | --- |
| Linux and WSL | `~/.config/easy-sso/rds-ca-aws.pem` |
| macOS | `~/Library/Application Support/easy-sso/rds-ca-aws.pem` |
| Native Windows | `%APPDATA%\easy-sso\rds-ca-aws.pem` |

GovCloud bookmarks use `rds-ca-aws-us-gov.pem` in the same folder, and `--state-dir` moves the folder. The **CA file** row shows the path and its state, such as `(cached)`, `(cached; refresh failed: …)` when an older valid file is still in place, or `(unavailable: …; Databases > Edit (e) > RDS CA file)`. A CA failure never blocks or changes the tunnel; it only changes that text and the status (`CA bundle unavailable: … Fix: Databases > Edit (e) > RDS CA file.`). Set **RDS CA file** to an existing PEM bundle to work offline or to use your own copy; easy-sso then only checks it. China (`aws-cn`) bookmarks need an explicit file: the row reads `aws-cn needs an RDS CA file (Databases > Edit)`, and `C` copies the URI without `tlsCAFile`.

**Elastic clusters.** Clusters with a `.docdb-elastic.amazonaws.com` endpoint need no CA file: AWS's DataGrip guide says to keep **Use SSL** checked and leave the CA file out, and easy-sso shows no CA path for them. An elastic cluster can also have a `.docdb.amazonaws.com` host; easy-sso cannot tell it apart and shows a CA row, so for such an elastic cluster leave the CA file out anyway. See [DocumentDB TLS](https://docs.aws.amazon.com/documentdb/latest/developerguide/security.encryption.ssl.html) and [AWS CA bundles](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.SSL.html).

**DataGrip.** Open the tunnel first, then choose **File > New > Data Source > Amazon DocumentDB** ([JetBrains guide](https://www.jetbrains.com/help/datagrip/documentdb.html)):

| Tab | Field | Value |
| --- | --- | --- |
| General | Host / Port | `127.0.0.1` and the tunnel's local port (37017 by default) |
| General | Authentication | **User & Password**, with your read-only or read-write DocumentDB user, saved in DataGrip |
| General | Database | blank |
| General | URL | press `c` in easy-sso and paste |
| General | Read preference, Replica set | empty |
| SSH/SSL | Use SSL | on |
| SSH/SSL | CA file | the **CA file** row (on WSL, the Windows path; see below). Elastic: none |
| SSH/SSL | Client certificate file, Client key file | empty |
| SSH/SSL | Mode | **Verify CA** |
| SSH/SSL | Use SSH tunnel | off; easy-sso is the tunnel |

`c` copies the DataGrip URL, for example:

```text
mongodb://127.0.0.1:37017/?directConnection=true&retryWrites=false&authSource=admin&tlsAllowInvalidHostnames=true
```

It never carries `tlsCAFile`: the bundle goes in DataGrip's **CA file** field, and TLS is DataGrip's **Use SSL** setting. With Verify CA and the CA file, DataGrip checks the certificate chain; the hostname check is off because the route is `127.0.0.1`. Each option has a reason:

| Option | Why |
| --- | --- |
| `directConnection=true` | Talk to the one host behind the tunnel instead of discovering replica set members. AWS says connecting in replica set mode (`replicaSet=rs0`) through a tunnel results in an error. |
| `retryWrites=false` | Engines before 8.0.2 reject retryable writes with error 301; elastic clusters and clusters on query planner 1.0 do not support them either ([retryable writes](https://docs.aws.amazon.com/documentdb/latest/developerguide/retryable-writes.html)). |
| `authSource=admin` | DocumentDB has no authentication database; every login happens in `admin`. This matters once a database name is in the URL. |
| `tlsAllowInvalidHostnames=true` | The certificate names the cluster endpoint, but the client connects to `127.0.0.1`. AWS's own tunnel example turns the hostname check off the same way; the chain is still checked against the CA file. |

easy-sso has no engine version setting. `retryWrites=false` works on every engine; on 8.0.2 and later (not elastic, not planner 1.0) you may remove it to get automatic write retries back.

**Reader endpoints are read-only.** With `directConnection=true`, a connection to the reader endpoint (`.cluster-ro-` in the host) is read-only for every user, including a read-write user. easy-sso adds `Reader endpoint: read-only for every user; use the cluster endpoint to write.` to the steps. Use the cluster endpoint, such as `example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com`, to write.

**Other clients.** `C` copies a URI with TLS and the CA file for mongosh, Compass and drivers, with the CA path URL-encoded:

```text
mongodb://127.0.0.1:37017/?tls=true&directConnection=true&retryWrites=false&authSource=admin&tlsAllowInvalidHostnames=true&tlsCAFile=<encoded CA path>
```

The status reads `Copied client URI with tlsCAFile <path>. In a shell, wrap it in single quotes.` The `&` characters split the command otherwise.

- **mongosh:** `mongosh '<pasted URI>' --username <your DocumentDB user>` prompts for the password and hides it as you type. mongosh officially supports MongoDB server 7.0 and later. DocumentDB 5.0, 8.0 and elastic clusters work with it in practice; 3.6 and 4.0 do not work at all. mongosh enables retryable writes by default, so keep `retryWrites=false` in the URI.
- **Compass:** paste the URI, or set the fields from MongoDB's Compass documentation: **Direct Connection** on the General tab, then on the TLS/SSL tab set TLS to **On**, **Certificate Authority** to the CA file and tick **tlsAllowInvalidHostnames**. AWS does not document Compass with DocumentDB.
- **Drivers:** the URI carries no user; give the driver your user and password through its credential options. AWS documents `tlsCAFile` with the bundle for Python and Node, and a custom TLS setup for Go, Java and C# ([connecting programmatically](https://docs.aws.amazon.com/documentdb/latest/developerguide/connect_programmatically.html)).
- **Java truststore fallback (Java and DataGrip):** if a Java client or DataGrip does not accept the PEM bundle, build a JKS truststore from the bundle with the `keytool` script in AWS's Java instructions. In DataGrip, add `-Djavax.net.ssl.trustStore=<path to truststore.jks> -Djavax.net.ssl.trustStorePassword=<truststore password>` to **Advanced > VM options** for that data source, and keep `tlsAllowInvalidHostnames=true` in the URL.

### WSL (Windows DataGrip, easysso in WSL)

This setup follows Microsoft's WSL networking documentation; it has not yet been tested end to end on a live cluster.

When easysso runs inside WSL and DataGrip runs on Windows, the tunnel listens on `127.0.0.1` inside WSL and DataGrip connects to the same `127.0.0.1:<port>` from Windows. Microsoft documents two ways this reaches Windows:

- **NAT mode** (the default): `localhostForwarding`, on by default, makes ports bound to localhost in WSL connectable from Windows. Setting `localhostForwarding=false` in `.wslconfig` stops Windows clients from reaching the tunnel.
- **Mirrored mode** (`networkingMode=mirrored`, Windows 11 22H2 and later): Windows and WSL share `127.0.0.1`.

Use `127.0.0.1`, never `localhost`: mirrored mode does not support the IPv6 localhost address `::1`. See [WSL networking](https://learn.microsoft.com/en-us/windows/wsl/networking) and [WSL settings](https://learn.microsoft.com/en-us/windows/wsl/wsl-config).

**CA file for Windows clients.** Under WSL the **DATAGRIP (or any MongoDB client)** block shows two rows: **CA file Windows** first, then **CA file WSL** (the Linux path, for tools inside WSL). Press `x` in Databases, the connection review, or on an open run in Sessions, Tunnels or run details. It copies the CA bundle to `%USERPROFILE%\.easy-sso\rds-ca-aws.pem` and puts that Windows path on the clipboard: `Copied the CA file to <path> and its path to the clipboard. DataGrip: SSH/SSL > CA file, paste, Mode Verify CA.` In Databases, `x` needs no open tunnel. The row then shows the path with `(copied by x)`, and pressing `x` again reports `Windows CA file is up to date: <path>. Path copied to the clipboard.`

Earlier exports are refreshed automatically: whenever easysso checks the bundle (tunnel open, `v`, `C`) and AWS has changed it, the Windows copy is rewritten, and after a restart easysso finds the earlier export and keeps it current, so DataGrip keeps reading the same path. The file is created in your Windows profile folder, which by default only you, SYSTEM and administrators can change. A custom **RDS CA file** is exported under its own file name; one already on a Windows drive (`/mnt/<drive>/…`) is not copied, and `x` only copies its Windows path. Elastic clusters need no CA file (`Elastic clusters need no CA file; keep Use SSL on.`). Outside WSL, `x` reports `x is for WSL; clients here read the CA file directly: <path>.` If Windows holds the file open during a refresh, close the DataGrip connection and press `x` again.

Before an export, the **CA file Windows** row shows the WSL share path, `\\wsl.localhost\<distro>\…\.config\easy-sso\rds-ca-aws.pem · x copies it to Windows`. `\\wsl.localhost` can start the distribution on Windows 11; the older `\\wsl$\<distro>\…` form works only while the distribution is running. The cached file is private to your Linux user, so prefer `x` if DataGrip cannot read the share path. Under WSL, `C` puts the exported Windows path (or else the share path) into `tlsCAFile`, because the clients run on Windows; edit it if you run mongosh inside WSL. `c` ends its status with the CA file to use, such as `CA file: <path>.`

**When WSL interop is off**, `x` and the copy keys cannot run Windows commands. `x` reports `Windows home folder not found (is WSL interop on?). Copy it yourself: cp '<source>' /mnt/c/certs/`. Copy the file yourself and use `C:\certs\rds-ca-aws.pem` as the CA file:

```sh
mkdir -p /mnt/c/certs
cp ~/.config/easy-sso/rds-ca-aws.pem /mnt/c/certs/
```

A manual copy is not refreshed; repeat it after AWS changes the bundle. Prefer a folder inside your Windows profile; folders such as `C:\certs` may be writable by other local users.

**Ports and firewall.** In NAT mode, easysso checks only Linux ports, so a Windows program already listening on the tunnel's local port is not detected, and DataGrip may reach that program instead. Pick another local port with `p` in the connection review or **Edit (e)**. Windows 11 22H2 and later with WSL 2.0.9 and later filter WSL traffic through the Hyper-V firewall. Loopback traffic between Windows and WSL is allowed by default, but managed laptops can change that; ask your IT team if Windows clients cannot connect while tools inside WSL can. See the [Hyper-V firewall](https://learn.microsoft.com/en-us/windows/security/operating-system-security/network-security/windows-firewall/hyper-v-firewall).

## Troubleshooting

### Unsafe permissions on `~/.aws`

**Symptom:** login stops with `Error: unsafe permissions on "/home/<you>/.aws" (mode 755); fix with: chmod 700 "/home/<you>/.aws"`, or `easysso diagnostics` lists an `AWS files directory` problem. Releases before 1.1.1 showed `unsafe permissions on application storage; require current-user-only access` only after the browser sign-in had completed.

**Cause:** easy-sso writes temporary role credentials only into a folder that no other local user can read. `aws configure sso` usually creates `~/.aws` with mode `755` (readable by everyone), and some WSL setups use a `000` umask that makes it `777`. easy-sso refuses rather than changing permissions on its own.

**Fix:**

```sh
chmod 700 ~/.aws
[ -f ~/.aws/credentials ] && chmod 600 ~/.aws/credentials
easysso diagnostics   # the AWS files directory problem should be gone
```

Then log in again. A refused login publishes nothing and leaves no cleanup pending, so no other repair is needed. If `AWS_CONFIG_FILE` or `AWS_SHARED_CREDENTIALS_FILE` point elsewhere, apply the same `chmod` to the folders that contain those files.

**WSL:** keep AWS files in the Linux home, not under `/mnt/c`. Windows drives usually ignore `chmod` and report every file as `777`, so the check cannot pass there. If `umask` prints `0000`, add `umask 022` to `~/.bashrc` or `~/.zshrc` so new folders are not created world-writable.

The same rule applies to the application state folder (`~/.config/easy-sso` on Linux): `chmod 700` it if startup reports it as unsafe.

### Session document refused or denied

**Symptom:** saving an EC2 bookmark reports `... is not a shell session document`, `session document must be a document name ...` or `session document ARN must be in the bookmark's partition and region`. Or a shell fails with `Access denied: ... session document (<name>)`.

**Cause:** the field takes a shell document only, by name or by an ARN in the instance's partition and region. Access denied means the role's `ssm:StartSession` policy does not allow that document for the instance, or the document is not shared with the account.

**Fix:** clear the field to use the Session Manager default, correct the name or ARN, or ask your AWS administrator to allow the document in the role's `ssm:StartSession` policy. To use an older easy-sso release, first clear the session document from every bookmark.

### DocumentDB clients

- **`PKIX path building failed` or `unable to find valid certification path` in DataGrip:** check that **CA file** points at the bundle from the **CA file** row (the Windows path under WSL) and that **Mode** is **Verify CA**. If it still fails, use the Java truststore fallback above.
- **Certificate hostname mismatch:** the URL needs `tlsAllowInvalidHostnames=true`. Paste the URL from `c` again instead of typing it.
- **`Driver class 'com.dbschema.MongoJdbcDriver' not found`:** add the MongoDB driver to the data source manually in DataGrip. On DataGrip 2026.1, JetBrains recommends MongoDB driver version 1.21 rather than 1.20.
- **Replica set errors:** remove `replicaSet=rs0` from the URL and leave DataGrip's **Replica set** field empty. Through a tunnel, connect with `directConnection=true`.
- **Error 301 or a retryable writes error:** keep `retryWrites=false` in the URL. mongosh enables retryable writes unless the URI turns them off.
- **Writes fail for a read-write user:** check for `.cluster-ro-` in the endpoint. Reader endpoints are read-only for every user; save a bookmark for the cluster endpoint to write.
- **Not-primary write errors after a failover:** the cluster endpoint moves to the new primary through DNS, and AWS does not document when the relay resolves it again. Stop the tunnel in **Tunnels** and open it again, then reconnect the client (not yet tested on a live cluster).
- **Login fails with a password that contains `%` followed by two characters:** DataGrip's driver encodes the user and password itself. Set `auto_encode_username_and_password=false` on the data source's **Advanced** tab and type each `%` as `%25`.
- **Elastic cluster, but a CA row is shown:** its host ends in `.docdb.amazonaws.com`, so easy-sso cannot tell it is elastic. Leave the CA file out and keep **Use SSL** on.
- **`Could not copy (install xclip or wl-clipboard on Linux). The URL is in Details (v).`:** install a clipboard tool, or select the URL line in the details by hand. Under WSL, the copy keys need WSL interop.
- **WSL: Windows DataGrip cannot connect or cannot read the CA file:** use `127.0.0.1`, press `x` and paste the Windows path into **CA file**, and check `localhostForwarding`, the local port and the firewall as described in [WSL](#wsl-windows-datagrip-easysso-in-wsl).
