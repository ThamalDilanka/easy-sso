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
	"github.com/ThamalDilanka/easy-sso/internal/launch"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

const documentDBLogin = "Tunnel only · your DocumentDB user stays in your client"

// documentDBIAMLogin labels a docdb-iam bookmark: the $external user is the role ARN.
const documentDBIAMLogin = "DocumentDB IAM · user is your role ARN"

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
	// quietRun marks an in-flight background export; queued is an explicit
	// x pressed meanwhile, run as soon as that export lands.
	quietRun         bool
	queued           *domain.Connection
	err, win, winErr string
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
		uri += "&tlsCAFile=" + escapeCA(ca)
	}
	return uri
}

// escapeCA writes a space as %20, never +, so shells and drivers read the
// same path.
func escapeCA(ca string) string { return strings.ReplaceAll(url.QueryEscape(ca), "+", "%20") }

// documentDBIAMOptions log in as the role ARN in $external. The role keys come
// from the client's environment; a URL never carries a user, key or token.
const documentDBIAMOptions = "directConnection=true&retryWrites=false&authSource=%24external&authMechanism=MONGODB-AWS&tlsAllowInvalidHostnames=true"

// dataGripIAMURL is the URL-only DataGrip data source of a docdb-iam run; TLS
// stays in DataGrip's SSL settings.
func dataGripIAMURL(port int) string {
	return fmt.Sprintf("mongodb://127.0.0.1:%d/?%s", port, documentDBIAMOptions)
}

// clientIAMURI is the docdb-iam URI for mongosh, Compass and drivers.
func clientIAMURI(port int, ca string) string {
	uri := fmt.Sprintf("mongodb://127.0.0.1:%d/?tls=true&%s", port, documentDBIAMOptions)
	if ca != "" {
		uri += "&tlsCAFile=" + escapeCA(ca)
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

// iamRun finds the newest live docdb-iam run of bookmark c: same route and
// database. A tunnel-mode run on the same route is not one.
func (m *Model) iamRun(c domain.Connection) (domain.Run, bool) {
	for i := len(m.runs) - 1; i >= 0; i-- {
		if r := m.runs[i]; r.Connection.DocDBIAM() && activeTunnel(r) && sameDocumentDBRoute(r.Connection, c) && r.Connection.Database == c.Database {
			return r, true
		}
	}
	return domain.Run{}, false
}

// documentDBPort is the port of an open tunnel on the same route, else the
// bookmark's own local port. A docdb-iam bookmark uses its own live run.
func (m *Model) documentDBPort(c domain.Connection) int {
	if c.DocDBIAM() {
		if r, ok := m.iamRun(c); ok {
			return r.Connection.LocalPort
		}
		return c.LocalPort
	}
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

func (m *Model) receiveExport(x caExportMsg) tea.Cmd {
	r := m.ca[x.path]
	queued := r.queued
	r.exporting, r.probed, r.quietRun, r.queued = false, true, false, nil
	if x.err == nil && x.exp.Exists {
		r.win, r.winErr = x.exp.Windows, ""
	} else if x.err != nil && x.quiet && r.win != "" {
		r.winErr = x.err.Error()
	}
	m.ca[x.path] = r
	if !x.quiet {
		m.status = x.feedback
	}
	if queued != nil {
		return m.exportCA(*queued, x.path, false)
	}
	return nil
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
		// An explicit x waits for a background export instead of vanishing.
		if !quiet && r.quietRun {
			r.queued = &c
			m.ca[path] = r
			m.status = "Copying the CA file to Windows..."
		}
		return nil
	}
	r.exporting, r.quietRun = true, quiet
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
				stale = "CA refresh failed; copied the cached bundle. "
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
			msg.feedback = "Copied the CA file and its path: " + win + ". Paste it into DataGrip SSH/SSL > CA file."
		case msg.exp.Changed:
			msg.feedback = "Copied the CA file to " + win + ". Could not copy its path; it is in Details (v)."
		case copied:
			msg.feedback = "Windows CA file is up to date: " + win + ". Path copied to the clipboard."
		default:
			msg.feedback = "Windows CA file is up to date: " + win + ". Could not copy its path; it is in Details (v)."
		}
		msg.feedback = stale + msg.feedback
		return msg
	}
}

func exportFailure(err error, win, src string) string {
	switch {
	case errors.Is(err, database.ErrNoWindowsHome):
		return "Windows home folder not found (is WSL interop on?). Copy it yourself: mkdir -p /mnt/c/certs && cp '" + src + "' /mnt/c/certs/"
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

// documentDBIAMClient is the docdb-iam guide: DataGrip with a URL-only data
// source and no auth, the role ARN as the $external user. It never shows a key.
func (m *Model) documentDBIAMClient(c domain.Connection, port int, user string) []string {
	lines := m.detailSection("DATAGRIP OR COMPASS (IAM LOGIN)", [][2]string{
		{"Login", "IAM (MONGODB-AWS), no password"},
		{"DB user", user},
		{"DataGrip", "URL only, Authentication No auth, then g to start it"},
		{"SSH/SSL", "Use SSL on · CA file below · Mode Verify CA · SSH tunnel off"},
	})
	path, err := m.caPath(c)
	lines = append(lines, m.caRows(c, path, err)...)
	lines = append(lines, m.detailRow("Admin", "createUser in $external with mechanisms MONGODB-AWS (v shows the command)"))
	lines = append(lines, m.detailRow("Other clients", "g starts Compass too · C copies URI for mongosh/Compass/drivers"))
	// The URL stands alone on its line so it can be selected by hand.
	lines = append(lines, "URL (c copies):", dataGripIAMURL(port))
	lines = append(lines, "With Verify CA and the CA file, DataGrip checks the certificate chain; the hostname check is off because the route is 127.0.0.1.")
	if strings.Contains(strings.ToLower(c.Endpoint), ".cluster-ro-") {
		lines = append(lines, "Reader endpoint: read-only for every user; use the cluster endpoint to write.")
	}
	return lines
}

// documentDBIAMAdmin is the one-time admin setup for a role. arn is empty when
// it is not known yet.
func documentDBIAMAdmin(c domain.Connection, arn string) []string {
	if arn == "" {
		arn = "<role ARN>"
	}
	db := c.Database
	if db == "" {
		db = "<db>"
	}
	return []string{
		"Admin setup, once per role (an admin user runs it in mongosh):",
		`db.getSiblingDB("$external").runCommand({createUser: "` + arn + `", mechanisms: ["MONGODB-AWS"], roles: [{role: "read", db: "` + db + `"}]})`,
		"5.0 instance-based; not elastic; never the primary user.",
		"Login proof: db.runCommand({connectionStatus:1}) must show your role ARN.",
	}
}

// reviewRoleARN is the $external user of the selected profile's active role,
// shown in a bookmark review, and the ARN alone ("" when unknown).
func (m *Model) reviewRoleARN(p domain.Profile) (string, string) {
	for _, r := range m.roles {
		if sameProfile(r.Profile, p) && roleActive(r) {
			arn, err := profiles.IAMRoleARN(r.Identity)
			if err != nil {
				return "Unavailable for this identity", ""
			}
			return arn, arn
		}
	}
	return "Log in to resolve your role ARN", ""
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

func copyFailure(generic bool) string {
	text := "Could not copy. "
	if goos == "linux" && getenv("WSL_DISTRO_NAME") == "" {
		text = "Could not copy (install xclip or wl-clipboard on Linux). "
	}
	if generic {
		return text + "For other clients add tls=true&tlsCAFile=<CA file row> to the URL in Details (v)."
	}
	return text + "The URL is in Details (v)."
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
	iam := c.DocDBIAM()
	if !generic && iam {
		feedback := fmt.Sprintf("Copied DataGrip URL for 127.0.0.1:%d. Data source: URL only, Authentication No auth, Use SSL with the CA file; g starts DataGrip.", port)
		if path, err := m.caPath(c); err == nil && wsl() {
			feedback = fmt.Sprintf("Copied DataGrip URL for 127.0.0.1:%d. CA file: %s · Mode Verify CA · URL only, No auth; g starts DataGrip.", port, m.windowsCAHint(path))
		}
		url := dataGripIAMURL(port)
		return func() tea.Msg {
			if !write(url) {
				return clipboardMsg{copyFailure(false)}
			}
			return clipboardMsg{feedback}
		}
	}
	if !generic {
		feedback := fmt.Sprintf("Copied DataGrip URL for 127.0.0.1:%d. In SSH/SSL set the CA file and Verify CA; enter your DocumentDB user in DataGrip.", port)
		if elastic {
			feedback = fmt.Sprintf("Copied DataGrip URL for 127.0.0.1:%d. In SSH/SSL turn on Use SSL with no CA file; enter your DocumentDB user in DataGrip.", port)
		} else if path, err := m.caPath(c); err == nil && wsl() {
			// Under WSL the Windows CA path comes first so the status bar keeps it.
			feedback = fmt.Sprintf("Copied DataGrip URL for 127.0.0.1:%d. CA file: %s · Mode Verify CA · log in with your DocumentDB user.", port, m.windowsCAHint(path))
		}
		url := dataGripURL(port)
		return func() tea.Msg {
			if !write(url) {
				return clipboardMsg{copyFailure(false)}
			}
			return clipboardMsg{feedback}
		}
	}
	uri := func(ca string, elastic bool) string {
		if iam {
			return clientIAMURI(port, ca)
		}
		return clientURI(port, ca, elastic)
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
			if !write(uri("", true)) {
				return clipboardMsg{copyFailure(true)}
			}
			return clipboardMsg{"Copied client URI (elastic clusters need no tlsCAFile). In a shell, wrap it in single quotes."}
		}
		if pathErr != nil {
			if !write(uri("", false)) {
				return clipboardMsg{copyFailure(true)}
			}
			return clipboardMsg{"Copied without tlsCAFile; set RDS CA file for aws-cn."}
		}
		_, err := ensure(context.Background(), c)
		msg := caCopyMsg{path: path, err: err, stale: err != nil && database.CachedCA(path)}
		if !write(uri(ca, false)) {
			msg.feedback = copyFailure(true)
			return msg
		}
		// A CA problem leads, so the two-line status bar never hides it.
		switch {
		case err != nil && msg.stale:
			msg.feedback = "CA refresh failed; the cached bundle still works. Copied client URI with tlsCAFile " + ca + "."
		case err != nil:
			msg.feedback = "CA bundle unavailable: " + err.Error() + ". Copied the URI anyway; its tlsCAFile " + ca + " may not exist yet. Fix: Databases > Edit (e) > RDS CA file."
		default:
			msg.feedback = "Copied client URI with tlsCAFile " + ca + ". In a shell, wrap it in single quotes."
		}
		return msg
	}
}

// windowsCAHint is the CA file a Windows DataGrip reads under WSL: the
// exported copy, the drive path, or a hint to export it with x.
func (m *Model) windowsCAHint(path string) string {
	ca := wslPath(path)
	switch {
	case m.ca[path].win != "":
		ca = m.ca[path].win
	case strings.HasPrefix(ca, `\\`):
		ca = "press x to copy it to Windows"
	}
	return ca
}

// The sessions service may offer these; the UI never holds a launcher or a
// snapshot (see domain.SessionService).
type clientLister interface {
	ClientApps(context.Context) ([]launch.Install, error)
}
type clientLauncher interface {
	LaunchClientApp(ctx context.Context, runID string, in launch.Install) (time.Time, error)
}

// clientAppsMsg reports the database clients found for a g press.
type clientAppsMsg struct {
	request  uint64
	run      domain.Run
	installs []launch.Install
	err      error
}

// clientLaunchMsg reports a client start: the credentials' expiry, or a fixed
// failure text. saveErr is a failed save of the place choice.
type clientLaunchMsg struct {
	request       uint64
	name, profile string
	expiry        time.Time
	err, saveErr  error
}

// Fixed g texts.
const (
	launchNoRun       = "No connected DocumentDB IAM run here. Connect the bookmark in Databases (Enter), then press g."
	launchNotIAM      = "g starts DataGrip or Compass for DocumentDB IAM connections only."
	launchUnavailable = "Starting DataGrip or Compass is not available in this build."
	launchPending     = "A database client lookup or start is still running."
)

// clientName is the display name of a client app.
func clientName(app launch.App) string {
	if app == launch.Compass {
		return "MongoDB Compass"
	}
	return "DataGrip"
}

// clientKey picks app in the client picker.
func clientKey(app launch.App) string {
	if app == launch.Compass {
		return "m"
	}
	return "d"
}

// foundApps lists each app found at least once, DataGrip first.
func foundApps(installs []launch.Install) []launch.App {
	var apps []launch.App
	for _, app := range []launch.App{launch.DataGrip, launch.Compass} {
		for _, in := range installs {
			if in.App == app {
				apps = append(apps, app)
				break
			}
		}
	}
	return apps
}

// placeInstall is the first install of app at place.
func placeInstall(installs []launch.Install, app launch.App, place launch.Place) (launch.Install, bool) {
	for _, in := range installs {
		if in.App == app && in.Place == place {
			return in, true
		}
	}
	return launch.Install{}, false
}

// placeName labels where an install lives: under WSL a local install is the
// WSL one.
func placeName(place launch.Place) string {
	switch {
	case place == launch.Windows:
		return "Windows"
	case wsl():
		return "WSL"
	}
	return "this machine"
}

// launchTarget resolves the run g acts on: the review first, then the selected
// row. ok is false when g has no meaning here; status then names the refusal.
func (m *Model) launchTarget() (run domain.Run, status string, ok bool) {
	fromBookmark := func(c domain.Connection) (domain.Run, string, bool) {
		if c.Kind != "db" {
			return domain.Run{}, "", false
		}
		if !c.DocDBIAM() {
			return domain.Run{}, launchNotIAM, false
		}
		if r, found := m.iamRun(c); found {
			return r, "", true
		}
		return domain.Run{}, launchNoRun, false
	}
	fromRun := func(r domain.Run) (domain.Run, string, bool) {
		if r.Connection.Kind != "db" {
			return domain.Run{}, "", false
		}
		if !r.Connection.DocDBIAM() {
			return domain.Run{}, launchNotIAM, false
		}
		if !activeTunnel(r) {
			return domain.Run{}, launchNoRun, false
		}
		return r, "", true
	}
	if r := m.review; r != nil {
		switch r.kind {
		case "connection":
			return fromBookmark(r.connection)
		case "run":
			return fromRun(r.run)
		}
		return domain.Run{}, "", false
	}
	switch m.screen {
	case databases:
		if ts := m.targets(); m.selectedRow >= 0 && m.selectedRow < len(ts) {
			return fromBookmark(ts[m.selectedRow])
		}
	case tunnels:
		if rs := m.visibleRuns(); m.selectedRow >= 0 && m.selectedRow < len(rs) {
			return fromRun(rs[m.selectedRow])
		}
	}
	return domain.Run{}, "", false
}

// canLaunch reports whether the Open client control belongs on this view.
func (m *Model) canLaunch() bool {
	_, _, ok := m.launchTarget()
	return ok
}

// launchKey handles g, from the keyboard and from the Open client control:
// look for DataGrip and Compass off the UI goroutine, then offer them.
func (m *Model) launchKey() tea.Cmd {
	run, status, ok := m.launchTarget()
	if !ok {
		if status != "" {
			m.status = status
		}
		return nil
	}
	if m.clientPending {
		m.status = launchPending
		return nil
	}
	lister, listOK := m.services.Sessions.(clientLister)
	if _, launchOK := m.services.Sessions.(clientLauncher); !listOK || !launchOK {
		m.status = launchUnavailable
		return nil
	}
	m.clientPending = true
	m.clientRequest++
	id := m.clientRequest
	m.status = "Looking for database clients..."
	return func() tea.Msg {
		// Finding a Windows client runs WSL interop, which can take a while.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		installs, err := lister.ClientApps(ctx)
		return clientAppsMsg{request: id, run: run, installs: installs, err: err}
	}
}

// receiveClientApps opens the client picker for the run g was pressed on.
func (m *Model) receiveClientApps(x clientAppsMsg) {
	if x.request != m.clientRequest {
		return
	}
	m.clientPending = false
	if x.err != nil {
		m.status = "Could not look for DataGrip or Compass."
		return
	}
	// Something else took the screen meanwhile; do not pop a picker over it.
	if m.busy || m.form != nil || m.help || m.picker || m.terminalID != "" || (m.review != nil && m.review.kind == "exit") {
		m.status = "Press g again to choose DataGrip or Compass."
		return
	}
	listScroll := m.scroll
	if m.review != nil {
		listScroll = m.review.listScroll
	}
	m.review = &review{kind: "client-app", run: x.run, installs: x.installs, listScroll: listScroll}
	m.scroll = 0
	m.status = ""
}

// clientAppKey handles the client picker.
func (m *Model) clientAppKey(key string) tea.Cmd {
	r := m.review
	apps := foundApps(r.installs)
	switch key {
	case "up", "k":
		if r.choice > 0 {
			r.choice--
		}
	case "down", "j":
		if r.choice+1 < len(apps) {
			r.choice++
		}
	case "d":
		return m.chooseClientApp(launch.DataGrip)
	case "m":
		return m.chooseClientApp(launch.Compass)
	case "enter":
		if len(apps) == 0 {
			return m.focusRun(r.run)
		}
		return m.chooseClientApp(apps[min(r.choice, len(apps)-1)])
	}
	return nil
}

// chooseClientApp starts app, asking for the place only under WSL when it is
// found on Windows and in WSL and no choice is saved.
func (m *Model) chooseClientApp(app launch.App) tea.Cmd {
	r := m.review
	local, hasLocal := placeInstall(r.installs, app, launch.Local)
	windows, hasWindows := placeInstall(r.installs, app, launch.Windows)
	switch {
	case !hasLocal && !hasWindows:
		m.status = clientName(app) + " was not found."
		return nil
	case hasLocal && hasWindows && wsl():
		switch launch.Place(m.clientPlaces[string(app)]) {
		case launch.Local:
			return m.startClient(r.run, local, false)
		case launch.Windows:
			return m.startClient(r.run, windows, false)
		}
		m.review = &review{kind: "client-place", run: r.run, installs: r.installs, app: app, listScroll: r.listScroll}
		return nil
	case hasLocal:
		return m.startClient(r.run, local, false)
	}
	return m.startClient(r.run, windows, false)
}

// clientPlaceKey handles the Windows or WSL choice; it is saved.
func (m *Model) clientPlaceKey(key string) tea.Cmd {
	r := m.review
	places := []launch.Place{launch.Windows, launch.Local}
	place := launch.Place("")
	switch key {
	case "up", "k":
		r.choice = 0
	case "down", "j":
		r.choice = 1
	case "w":
		place = launch.Windows
	case "l":
		place = launch.Local
	case "enter":
		place = places[min(max(r.choice, 0), 1)]
	}
	if place == "" {
		return nil
	}
	in, ok := placeInstall(r.installs, r.app, place)
	if !ok {
		return nil
	}
	if m.clientPlaces == nil {
		m.clientPlaces = map[string]string{}
	}
	m.clientPlaces[string(r.app)] = string(place)
	return m.startClient(r.run, in, true)
}

// startClient starts in with the run's frozen role credentials, which only
// the sessions service holds. It can block about 3 s, so it runs in a command.
func (m *Model) startClient(run domain.Run, in launch.Install, save bool) tea.Cmd {
	launcher, ok := m.services.Sessions.(clientLauncher)
	if !ok {
		m.status = launchUnavailable
		return nil
	}
	var saveChoice func(string, string) error
	if save {
		saveChoice = m.services.SaveClientPlace
	}
	if r := m.review; r != nil {
		m.scroll = r.listScroll
	}
	m.review = nil
	m.clientPending = true
	m.clientRequest++
	id, name, profile := m.clientRequest, clientName(in.App), run.Profile.Name
	m.status = "Starting " + name + "..."
	return func() tea.Msg {
		msg := clientLaunchMsg{request: id, name: name, profile: profile}
		if saveChoice != nil {
			msg.saveErr = saveChoice(string(in.App), string(in.Place))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg.expiry, msg.err = launcher.LaunchClientApp(ctx, run.ID, in)
		return msg
	}
}

// receiveClientLaunch shows the start result. Failure texts are fixed texts
// from the sessions service and never carry a key.
func (m *Model) receiveClientLaunch(x clientLaunchMsg) {
	if x.request != m.clientRequest {
		return
	}
	m.clientPending = false
	if x.err != nil {
		m.status = x.err.Error()
		return
	}
	m.status = fmt.Sprintf("%s started with role [%s] credentials until %s. It keeps them until it exits.", x.name, x.profile, x.expiry.Local().Format("15:04"))
	if x.saveErr != nil {
		m.status += " Could not save the Windows or WSL choice; g asks again after a restart."
	}
}
