package ui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/database"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

const documentDBLogin = "Tunnel only · your DocumentDB user stays in your client"

// Seams for platform-specific path rendering.
var goos = runtime.GOOS
var getenv = os.Getenv

// caMsg reports a CA bundle check; stale means a valid older file remains.
type caMsg struct {
	path  string
	err   error
	stale bool
}

// caResult is the UI state of one CA bundle path. win is its exported
// Windows copy (WSL only); probed means an earlier export was looked for.
type caResult struct {
	ready, inflight, stale bool
	exporting, probed      bool
	err, win, winErr       string
}

// caExportMsg reports an x export, or a quiet refresh of an earlier one.
type caExportMsg struct {
	path     string
	exp      domain.CAExport
	err      error
	quiet    bool
	feedback string
}

func wsl() bool { return goos == "linux" && getenv("WSL_DISTRO_NAME") != "" }

func isDocumentDB(c domain.Connection) bool {
	return c.Kind == "db" && connections.DocumentDBEndpoint(c.Endpoint)
}

// localRoute names a tunnel's local end. DocumentDB clients must use
// 127.0.0.1 (WSL mirrored mode does not forward ::1), so its rows say so.
func localRoute(c domain.Connection) string {
	if isDocumentDB(c) {
		return fmt.Sprintf("127.0.0.1:%d", c.LocalPort)
	}
	return fmt.Sprintf("localhost:%d", c.LocalPort)
}

func engineLabel(c domain.Connection) string {
	if isDocumentDB(c) {
		return "DocumentDB"
	}
	return "PostgreSQL / other"
}

const documentDBOptions = "directConnection=true&retryWrites=false&authSource=admin&tlsAllowInvalidHostnames=true"

// dataGripURL leaves TLS to DataGrip's Use SSL setting and its CA file field.
func dataGripURL(port int) string {
	return fmt.Sprintf("mongodb://127.0.0.1:%d/?%s", port, documentDBOptions)
}

// clientURI is for mongosh, Compass and drivers. It never carries a user.
func clientURI(port int, ca string, elastic bool) string {
	uri := fmt.Sprintf("mongodb://127.0.0.1:%d/?tls=true&%s", port, documentDBOptions)
	if ca != "" && !elastic {
		uri += "&tlsCAFile=" + strings.ReplaceAll(url.QueryEscape(ca), "+", "%20")
	}
	return uri
}

// wslPath is the Windows view of a WSL path, or "" outside WSL.
func wslPath(p string) string {
	distro := getenv("WSL_DISTRO_NAME")
	if goos != "linux" || distro == "" || !strings.HasPrefix(p, "/") {
		return ""
	}
	if rest, ok := strings.CutPrefix(p, "/mnt/"); ok && len(rest) >= 1 && (len(rest) == 1 || rest[1] == '/') && rest[0] >= 'a' && rest[0] <= 'z' {
		return strings.ToUpper(rest[:1]) + ":\\" + strings.ReplaceAll(strings.TrimPrefix(rest[1:], "/"), "/", "\\")
	}
	return `\\wsl.localhost\` + distro + strings.ReplaceAll(p, "/", "\\")
}

func activeTunnel(r domain.Run) bool {
	return strings.EqualFold(r.State, "Listening") || strings.EqualFold(r.State, "Connected")
}

func sameDocumentDBRoute(a, b domain.Connection) bool {
	return a.Kind == b.Kind && a.AccountID == b.AccountID && a.Partition == b.Partition && strings.EqualFold(a.Endpoint, b.Endpoint) && a.DBPort == b.DBPort && a.Region == b.Region && a.RelayInstanceID == b.RelayInstanceID && a.RelayRegion == b.RelayRegion
}

// openTunnel finds the newest open tunnel on the same route as c.
func (m *Model) openTunnel(c domain.Connection) (domain.Run, bool) {
	for i := len(m.runs) - 1; i >= 0; i-- {
		if r := m.runs[i]; activeTunnel(r) && sameDocumentDBRoute(r.Connection, c) {
			return r, true
		}
	}
	return domain.Run{}, false
}

// documentDBPort is the port of an open tunnel on the same route, else the
// bookmark's own local port.
func (m *Model) documentDBPort(c domain.Connection) int {
	if r, ok := m.openTunnel(c); ok {
		return r.Connection.LocalPort
	}
	return c.LocalPort
}

func (m *Model) caPath(c domain.Connection) (string, error) {
	return database.CAPath(c, m.services.Prerequisites.StateDir)
}

// ensureCA checks or downloads the CA bundle for a DocumentDB connection. A
// failure only changes text; it never touches the tunnel.
func (m *Model) ensureCA(c domain.Connection) tea.Cmd {
	if !isDocumentDB(c) || connections.DocumentDBElastic(c.Endpoint) {
		return nil
	}
	path, err := m.caPath(c)
	if err != nil || m.ca[path].inflight {
		return nil
	}
	r := m.ca[path]
	r.inflight = true
	m.ca[path] = r
	ensure := m.ensurer()
	return func() tea.Msg {
		_, err := ensure(context.Background(), c)
		return caMsg{path: path, err: err, stale: err != nil && database.CachedCA(path)}
	}
}

// ensurer returns the injected CA check or the default cached AWS bundle.
func (m *Model) ensurer() func(context.Context, domain.Connection) (string, error) {
	if ensure := m.services.EnsureCA; ensure != nil {
		return ensure
	}
	dir := m.services.Prerequisites.StateDir
	return func(ctx context.Context, c domain.Connection) (string, error) { return database.EnsureCA(ctx, c, dir) }
}

func (m *Model) receiveCA(x caMsg) tea.Cmd {
	r := m.ca[x.path]
	r.ready, r.inflight, r.stale, r.err = x.err == nil, false, x.stale, ""
	if x.err != nil {
		r.err = x.err.Error()
		// Only this bundle's tunnel announcement is rewritten, problem first so
		// the status bar never cuts it off. A stale bundle still works.
		if !x.stale && m.status != "" && m.status == m.caStatus && x.path == m.caStatusPath {
			m.status = fmt.Sprintf("Tunnel open at 127.0.0.1:%d · CA bundle unavailable: %s. Fix: Databases > Edit (e) > RDS CA file.", m.caStatusPort, r.err)
		}
	}
	m.ca[x.path] = r
	if x.err != nil {
		return nil
	}
	return m.refreshExport(x.path)
}

// refreshExport quietly looks for an earlier Windows export once, then keeps
// a found one in step with the bundle. It never creates a new export.
func (m *Model) refreshExport(path string) tea.Cmd {
	r := m.ca[path]
	if !strings.HasPrefix(wslPath(path), `\\`) || r.exporting || (r.probed && r.win == "") {
		return nil
	}
	return m.exportCA(domain.Connection{}, path, true)
}

func (m *Model) receiveExport(x caExportMsg) {
	r := m.ca[x.path]
	r.exporting, r.probed = false, true
	if x.err == nil && x.exp.Exists {
		r.win, r.winErr = x.exp.Windows, ""
	} else if x.err != nil && x.quiet && r.win != "" {
		r.winErr = x.err.Error()
	}
	m.ca[x.path] = r
	if !x.quiet {
		m.status = x.feedback
	}
}

func (m *Model) exporter() func(context.Context, string, bool) (domain.CAExport, error) {
	if export := m.services.ExportCA; export != nil {
		return export
	}
	return database.ExportCAToWindows
}

// exportCAKey handles x: copy the CA file to Windows under WSL and put its
// Windows path on the clipboard.
func (m *Model) exportCAKey(key string) (tea.Cmd, bool) {
	if key != "x" {
		return nil, false
	}
	c, _, ok := m.documentDBTarget()
	if !ok {
		return nil, false
	}
	path, err := m.caPath(c)
	switch {
	case connections.DocumentDBElastic(c.Endpoint):
		m.status = "Elastic clusters need no CA file; keep Use SSL on."
	case err != nil && c.Partition == "aws-cn":
		m.status = "aws-cn needs an RDS CA file (Databases > Edit) before x can copy it."
	case err != nil:
		m.status = "Could not copy the CA file to Windows: " + err.Error() + "."
	case !wsl():
		m.status = "x is for WSL; clients here read the CA file directly: " + path + "."
	case !strings.HasPrefix(wslPath(path), `\\`):
		win, copy := wslPath(path), m.copyText()
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if copy(ctx, win) != nil {
				return clipboardMsg{"CA file is already on Windows: " + win + ". Could not copy its path; it is in Details (v)."}
			}
			return clipboardMsg{"CA file is already on Windows: " + win + ". Path copied to the clipboard."}
		}, true
	default:
		return m.exportCA(c, path, false), true
	}
	return nil, true
}

// exportCA copies path to Windows. A quiet run only refreshes an earlier
// export and changes no status; an explicit one checks the bundle first and
// copies the Windows path.
func (m *Model) exportCA(c domain.Connection, path string, quiet bool) tea.Cmd {
	r := m.ca[path]
	if r.exporting {
		return nil
	}
	r.exporting = true
	m.ca[path] = r
	if !quiet {
		m.status = "Copying the CA file to Windows..."
	}
	export, ensure, copy := m.exporter(), m.ensurer(), m.copyText()
	return func() tea.Msg {
		msg := caExportMsg{path: path, quiet: quiet}
		stale := ""
		if !quiet {
			if _, err := ensure(context.Background(), c); err != nil {
				if !database.CachedCA(path) {
					msg.feedback = "CA bundle unavailable: " + err.Error() + "; nothing copied. Set Databases > Edit (e) > RDS CA file."
					return msg
				}
				stale = " CA refresh failed: " + err.Error() + "; copied the cached bundle."
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg.exp, msg.err = export(ctx, path, quiet)
		if quiet {
			return msg
		}
		if msg.err != nil {
			msg.feedback = exportFailure(msg.err, msg.exp.Windows, path)
			return msg
		}
		win := msg.exp.Windows
		ctx, cancelCopy := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelCopy()
		copied := copy(ctx, win) == nil
		switch {
		case msg.exp.Changed && copied:
			msg.feedback = "Copied the CA file to " + win + " and its path to the clipboard. DataGrip: SSH/SSL > CA file, paste, Mode Verify CA."
		case msg.exp.Changed:
			msg.feedback = "Copied the CA file to " + win + ". Could not copy its path; it is in Details (v)."
		case copied:
			msg.feedback = "Windows CA file is up to date: " + win + ". Path copied to the clipboard."
		default:
			msg.feedback = "Windows CA file is up to date: " + win + ". Could not copy its path; it is in Details (v)."
		}
		msg.feedback += stale
		return msg
	}
}

func exportFailure(err error, win, src string) string {
	switch {
	case errors.Is(err, database.ErrNoWindowsHome):
		return "Windows home folder not found (is WSL interop on?). Copy it yourself: cp '" + src + "' /mnt/c/certs/"
	case errors.Is(err, store.ErrLink):
		dir := "the Windows folder"
		if i := strings.LastIndex(win, `\`); i > 0 {
			dir = win[:i]
		}
		return "Refusing to write through a link in " + dir + "; remove it or copy the file yourself."
	case errors.Is(err, database.ErrNotCABundle):
		return win + " exists and is not a CA bundle; move it away, then press x again."
	case errors.Is(err, store.ErrInUse):
		return "Windows has " + win + " open; close the DataGrip connection, then press x again."
	}
	return "Could not copy the CA file to Windows: " + err.Error() + "."
}

// caRows renders the CA file rows. Under WSL the Windows path comes first,
// because DataGrip runs on Windows there.
func (m *Model) caRows(c domain.Connection, path string, pathErr error) []string {
	if pathErr != nil {
		if c.Partition == "aws-cn" {
			return []string{m.detailRow("CA file", "aws-cn needs an RDS CA file (Databases > Edit)")}
		}
		return []string{m.detailRow("CA file", pathErr.Error())}
	}
	r := m.ca[path]
	suffix := " (downloads when the tunnel opens)"
	if c.CAFile != "" {
		suffix = " (your RDS CA file)"
	}
	switch {
	case r.ready && c.CAFile == "":
		suffix = " (cached)"
	case r.err != "" && r.stale:
		suffix = " (cached; refresh failed: " + r.err + ")"
	case r.err != "":
		suffix = " (unavailable: " + r.err + "; Databases > Edit (e) > RDS CA file)"
	}
	if win := wslPath(path); win != "" {
		value := win
		switch {
		case r.win != "" && r.winErr != "":
			value = r.win + " (refresh failed: " + r.winErr + "; x retries)"
		case r.win != "":
			value = r.win + " (copied by x)"
		case strings.HasPrefix(win, `\\`):
			value = win + " · x copies it to Windows"
		case c.CAFile != "":
			value = win + " (your RDS CA file)"
		}
		return []string{m.detailRow("CA file Windows", value), m.detailRow("CA file WSL", path+suffix)}
	}
	return []string{m.detailRow("CA file", path+suffix)}
}

// documentDBClient is the DataGrip-first connection guide. It never shows or
// stores a database user or password.
func (m *Model) documentDBClient(c domain.Connection, port int) []string {
	elastic := connections.DocumentDBElastic(c.Endpoint)
	ssl := "Use SSL on · CA file below · Mode Verify CA · SSH tunnel off"
	if elastic {
		ssl = "Use SSL on · no CA file · SSH tunnel off"
	}
	lines := m.detailSection("DATAGRIP (or any MongoDB client)", [][2]string{
		{"Data source", "File > New > Data Source > Amazon DocumentDB"},
		{"Host / Port", fmt.Sprintf("127.0.0.1 / %d · Database blank", port)},
		{"Authentication", "User & Password: your read-only or read-write DocumentDB user, saved in DataGrip"},
		{"SSH/SSL", ssl},
	})
	if elastic {
		lines = append(lines, m.detailRow("CA file", "not needed for elastic clusters; keep Use SSL on"))
	} else {
		path, err := m.caPath(c)
		lines = append(lines, m.caRows(c, path, err)...)
	}
	lines = append(lines, m.detailRow("Users", "One tunnel serves every user: one data source each · C copies URI for mongosh/Compass/drivers"))
	// The URL stands alone on its line so it can be selected by hand.
	lines = append(lines, "URL (c copies):", dataGripURL(port))
	if elastic {
		lines = append(lines, "The hostname check is off because the route is 127.0.0.1.")
	} else {
		lines = append(lines, "With Verify CA and the CA file, DataGrip checks the certificate chain; the hostname check is off because the route is 127.0.0.1.")
	}
	lines = append(lines, "retryWrites=false works on every engine; 8.0.2+ (not elastic, not planner 1.0) may remove it.")
	if strings.Contains(strings.ToLower(c.Endpoint), ".cluster-ro-") {
		lines = append(lines, "Reader endpoint: read-only for every user; use the cluster endpoint to write.")
	}
	return lines
}

// caCopyMsg reports a C copy: its CA check result and the status to show.
type caCopyMsg struct {
	path     string
	err      error
	stale    bool
	feedback string
}

// documentDBTarget resolves the DocumentDB bookmark or open run that c and C
// act on: the review first, then the selected row.
func (m *Model) documentDBTarget() (domain.Connection, int, bool) {
	fromRun := func(r domain.Run) (domain.Connection, int, bool) {
		if !isDocumentDB(r.Connection) {
			return domain.Connection{}, 0, false
		}
		if !activeTunnel(r) {
			m.status = "Tunnel not open; Enter on the bookmark reopens it."
			return domain.Connection{}, 0, false
		}
		return r.Connection, r.Connection.LocalPort, true
	}
	if r := m.review; r != nil {
		switch r.kind {
		case "connection":
			if isDocumentDB(r.connection) {
				return r.connection, m.documentDBPort(r.connection), true
			}
		case "run":
			return fromRun(r.run)
		}
		return domain.Connection{}, 0, false
	}
	switch m.screen {
	case databases:
		if ts := m.targets(); m.selectedRow >= 0 && m.selectedRow < len(ts) && isDocumentDB(ts[m.selectedRow]) {
			return ts[m.selectedRow], m.documentDBPort(ts[m.selectedRow]), true
		}
	case sessions, tunnels:
		if rs := m.visibleRuns(); m.selectedRow >= 0 && m.selectedRow < len(rs) {
			return fromRun(rs[m.selectedRow])
		}
	}
	return domain.Connection{}, 0, false
}

// copyDocumentDBKey handles c (DataGrip URL) and C (client URI); it reports
// false when the target is not DocumentDB, so the key keeps its other uses.
func (m *Model) copyDocumentDBKey(key string) (tea.Cmd, bool) {
	if key != "c" && key != "C" {
		return nil, false
	}
	c, port, ok := m.documentDBTarget()
	if !ok {
		return nil, false
	}
	return m.copyDocumentDB(c, port, key == "C"), true
}

func copyFailure() string {
	if goos == "linux" && getenv("WSL_DISTRO_NAME") == "" {
		return "Could not copy (install xclip or wl-clipboard on Linux). The URL is in Details (v)."
	}
	return "Could not copy. The URL is in Details (v)."
}

// copyDocumentDB copies connection strings only; never a user or password.
func (m *Model) copyDocumentDB(c domain.Connection, port int, generic bool) tea.Cmd {
	copy := m.copyText()
	write := func(text string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return copy(ctx, text) == nil
	}
	elastic := connections.DocumentDBElastic(c.Endpoint)
	if !generic {
		feedback := fmt.Sprintf("Copied DataGrip URL for 127.0.0.1:%d. In SSH/SSL set the CA file and Verify CA; enter your DocumentDB user in DataGrip.", port)
		if elastic {
			feedback = fmt.Sprintf("Copied DataGrip URL for 127.0.0.1:%d. In SSH/SSL turn on Use SSL with no CA file; enter your DocumentDB user in DataGrip.", port)
		} else if path, err := m.caPath(c); err == nil && wsl() {
			switch win := wslPath(path); {
			case m.ca[path].win != "":
				feedback += " CA file: " + m.ca[path].win + "."
			case strings.HasPrefix(win, `\\`):
				feedback += " CA file: use the CA file Windows row (x copies it to Windows)."
			default:
				feedback += " CA file: " + win + "."
			}
		}
		url := dataGripURL(port)
		return func() tea.Msg {
			if !write(url) {
				return clipboardMsg{copyFailure()}
			}
			return clipboardMsg{feedback}
		}
	}
	path, pathErr := m.caPath(c)
	ensure := m.ensurer()
	// Under WSL the clients run on Windows, so they need a Windows path: the
	// exported copy if there is one, else the WSL share path.
	ca := path
	if win := m.ca[path].win; win != "" {
		ca = win
	} else if win := wslPath(path); win != "" {
		ca = win
	}
	return func() tea.Msg {
		if elastic {
			if !write(clientURI(port, "", true)) {
				return clipboardMsg{copyFailure()}
			}
			return clipboardMsg{"Copied client URI (elastic clusters need no tlsCAFile). In a shell, wrap it in single quotes."}
		}
		if pathErr != nil {
			if !write(clientURI(port, "", false)) {
				return clipboardMsg{copyFailure()}
			}
			return clipboardMsg{"Copied without tlsCAFile; set RDS CA file for aws-cn."}
		}
		_, err := ensure(context.Background(), c)
		msg := caCopyMsg{path: path, err: err, stale: err != nil && database.CachedCA(path)}
		if !write(clientURI(port, ca, false)) {
			msg.feedback = copyFailure()
			return msg
		}
		msg.feedback = "Copied client URI with tlsCAFile " + ca + ". In a shell, wrap it in single quotes."
		if err != nil {
			msg.feedback += " CA bundle unavailable: " + err.Error() + "."
		}
		return msg
	}
}
