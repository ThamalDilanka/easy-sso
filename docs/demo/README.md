# Offline browser demo

Open the hosted copy at <https://thamaldilanka.github.io/easy-sso/>, or download
[easy-sso-demo.html](easy-sso-demo.html) and open the saved file in your browser.
In a local checkout, double-click it or run `make demo` from the repo root (the
shortcut uses Python to open the file in your default browser).

That one HTML file contains all styles, scripts, and fictional fixtures. It needs
no server, build, Go, npm, AWS tools, credentials, or network connection.
GitHub's file viewer shows the source; download the file to interact with it.
GitHub Pages serves the same file unchanged as the site's `index.html`.

## Things to try

1. Connect while logged out to see the login warning. Click **Log in**, then
   **Connect terminal**. Type `help` in the simulated shell.
2. Return using **AWS workspace**, switch profiles, and inspect **Sessions**.
   Established runs retain the identity they started with.
3. In **Databases**, connect the PostgreSQL IAM bookmark or open the tunnel-only
   bookmark. Try **Set up IAM**, editing, and saving with Ctrl+S.
4. Try new-connection drafts, account labels (`y`), **Select roles**, credential
   renewal/removal, and stop reviews. **Help** lists keyboard shortcuts;
   buttons above the workspace provide browser-reserved shortcut alternatives.
5. In **Backup**, import `demo/bookmarks-backup.json`. Preview the addition and
   conflict, optionally replacing conflicts. Export to a new virtual filename,
   then import it. These files exist only in this tab's memory.
6. Choose **Scenario after reset**, then click **Reset demo** to apply it.
   Scenarios cover active/expired credentials, cleanup failure, and connection
   failure. For cleanup failure, use **Credentials → Retry cleanup**, then log in.
7. Press `q` for exit review. Reset starts a fresh workspace.

Optional URL parameters: `?theme=light&state=active`. Supported states are
`logged-out`, `active`, `expired`, `cleanup-failure`, and `connection-failure`.

## Boundaries and maintenance

All state stays in browser memory; reset/reload discards changes. There are no
runtime network requests, external assets, analytics, browser storage, or host
file/clipboard access. A restrictive content security policy also blocks network
connections. Shell commands have canned responses; no processes, authentication,
credentials, ports, or tunnels are created. OS terminal windows are unavailable.

Use only fictional data and reserved example endpoints. Never add or enter real
account IDs, personal information, credentials, or captures of real environments.
The fixtures follow `testdata/aws-config.ini`.

The layout, colors, keyboard navigation, forms, and reviews follow `internal/ui`.
This is a standalone browser simulation, not the Go renderer. Keep it aligned
when production UI behavior changes; it does not validate actual AWS behavior.
At narrow widths the workspace scrolls horizontally. Escape then Tab releases
keyboard focus from the workspace.

Validation:

```sh
python3 -m unittest discover -s tools -p 'test_*.py'
make check
```

For the browser interaction regression check, use an existing Playwright install
and Chromium (these are development test tools, not demo dependencies):

```sh
node tools/test_demo_browser.cjs
```

If Playwright is outside Node's module path, set `EASY_SSO_PLAYWRIGHT_MODULE` to
its package directory. If using a separate Chromium binary, set
`EASY_SSO_CHROMIUM` to its executable. The test opens the HTML directly with the
browser context offline and checks that no HTTP(S) requests occur.
