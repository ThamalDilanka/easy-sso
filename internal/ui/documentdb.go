package ui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/database"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
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

type caResult struct {
	ready, inflight, stale bool
	err                    string
}

func isDocumentDB(c domain.Connection) bool {
	return c.Kind == "db" && connections.DocumentDBEndpoint(c.Endpoint)
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

// documentDBPort is the port of an open tunnel on the same route, else the
// bookmark's own local port.
func (m *Model) documentDBPort(c domain.Connection) int {
	for i := len(m.runs) - 1; i >= 0; i-- {
		if r := m.runs[i]; activeTunnel(r) && sameDocumentDBRoute(r.Connection, c) {
			return r.Connection.LocalPort
		}
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
	ensure := m.services.EnsureCA
	if ensure == nil {
		dir := m.services.Prerequisites.StateDir
		ensure = func(ctx context.Context, c domain.Connection) (string, error) { return database.EnsureCA(ctx, c, dir) }
	}
	return func() tea.Msg {
		_, err := ensure(context.Background(), c)
		return caMsg{path: path, err: err, stale: err != nil && database.CachedCA(path)}
	}
}

func (m *Model) receiveCA(x caMsg) {
	r := caResult{ready: x.err == nil}
	if x.err != nil {
		r.err, r.stale = x.err.Error(), x.stale
		// Only the status that announced this tunnel gains the warning.
		if m.status != "" && m.status == m.caStatus {
			m.status += " CA bundle unavailable: " + r.err + "; set Databases > Edit (e) > RDS CA file."
		}
	}
	m.ca[x.path] = r
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
		return []string{m.detailRow("CA file Windows", win), m.detailRow("CA file WSL", path+suffix)}
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
		} else if wslPath("/") != "" {
			feedback += " CA file: use the CA file Windows row."
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
	ensure := m.services.EnsureCA
	if ensure == nil {
		dir := m.services.Prerequisites.StateDir
		ensure = func(ctx context.Context, c domain.Connection) (string, error) { return database.EnsureCA(ctx, c, dir) }
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
		// Under WSL the clients run on Windows, so they need the Windows path.
		ca := path
		if win := wslPath(path); win != "" {
			ca = win
		}
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
