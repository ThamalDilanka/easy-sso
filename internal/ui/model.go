// Package ui implements the keyboard-first workspace. Service calls run only in
// Bubble Tea commands, never during rendering or key handling.
package ui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/timer"
	tea "github.com/charmbracelet/bubbletea"
)

type screen int

const (
	ec2 screen = iota
	databases
	sessions
	tunnels
	credentials
	backup
)

var screenNames = []string{"EC2", "Databases", "Sessions", "Tunnels", "Credentials", "Backup"}

// field is one form row. id names the value it holds, so code reads a form
// by meaning and two forms may order or omit fields differently.
type field struct{ label, value, id string }
type form struct {
	original *domain.Connection
	kind     string
	// engine is engPostgres or engDocumentDB on a database form.
	engine string
	fields []field
	index  int
}

// Database engines a new database form is built for. The engine is never
// stored: a saved bookmark's engine follows from its endpoint and login mode.
const (
	engPostgres   = "postgres"
	engDocumentDB = "documentdb"
)

// databaseEngines is the order of the rows in the database type picker.
var databaseEngines = []string{engPostgres, engDocumentDB}

// get returns the trimmed value of the field with id, or "" if the form has
// no such field.
func (f *form) get(id string) string {
	for _, field := range f.fields {
		if field.id == id {
			return strings.TrimSpace(field.value)
		}
	}
	return ""
}

// set replaces the value of the field with id; a missing id changes nothing.
func (f *form) set(id, value string) {
	for i := range f.fields {
		if f.fields[i].id == id {
			f.fields[i].value = value
		}
	}
}

type review struct {
	listScroll int
	details    bool
	// choice is the highlighted row of a list review such as the database
	// type picker.
	choice                           int
	kind                             string
	connection                       domain.Connection
	run                              domain.Run
	role                             domain.ManagedRole
	selections                       []domain.Selection
	plan                             domain.ImportPlan
	acknowledged, replace, duplicate bool
}
type loadedMsg struct {
	epoch       uint64
	connections []domain.Connection
	roles       []domain.ManagedRole
	runs        []domain.Run
	err         error
}
type resultMsg struct {
	epoch, request      uint64
	action              string
	run                 domain.Run
	plan                domain.ImportPlan
	err                 error
	aliasID, aliasLabel string
}
type tickMsg time.Time
type profilesMsg struct {
	request  uint64
	profiles []domain.Profile
	err      error
}

// AuthNoticeMsg contains transient official CLI sign-in guidance, never persisted.
type AuthNoticeMsg string

// clipboardMsg reports the outcome of a clipboard write; it carries no secrets.
type clipboardMsg struct{ feedback string }

// Model exposes a normal Bubble Tea model, with all credentials kept in services.
// No access key or token is copied into UI state.
type Model struct {
	themeSaving                         bool
	themePending                        string
	input                               textinput.Model
	inputForm                           *form
	inputIndex                          int
	activitySpinner                     spinner.Model
	operationStarted                    time.Time
	operationButton                     string
	services                            domain.Services
	width, height                       int
	selectedProfile, selectedRow, focus int
	screen                              screen
	epoch, request                      uint64
	connections                         []domain.Connection
	roles                               []domain.ManagedRole
	expiryTimers                        map[string]expiryTimer
	runs                                []domain.Run
	form                                *form
	review                              *review
	drafts                              map[screen]form
	picker                              bool
	picked                              map[int]bool
	pickerRow                           int
	status                              string
	terminalFocus                       bool
	terminalID                          string
	terminalTabs                        []domain.Run
	pendingTarget                       string
	authNotice, authURL, authFeedback   string
	busy, help, ascii, noColor, dark    bool
	aliases                             map[string]string
	cancel                              context.CancelFunc
	scroll                              int
	exitAfterOperation                  bool
	profilesRequest                     uint64
	profilesLoading, profilesQueued     bool
	pendingProfiles                     *profilesMsg
	hits                                []hitRegion
	rowActions                          map[string][]mouseAction
	expanded                            map[string]bool
	sidebarScroll                       int
	maxScroll, maxSidebarScroll         int
	terminalArea                        paneArea
	reconnectFrom                       string
	// ca holds DocumentDB CA bundle checks by path; caStatus is the Listening
	// status a failed check may extend; pendingRoute selects a new bookmark.
	ca           map[string]caResult
	caStatus     string
	caStatusPath string
	caStatusPort int
	pendingRoute *domain.Connection
}

func New(s domain.Services) tea.Model {
	s.Profiles = orderedProfiles(s.Profiles)
	_, noColor := os.LookupEnv("NO_COLOR")
	m := &Model{services: s, width: 100, height: 30, selectedProfile: 0, focus: 1, drafts: make(map[screen]form), ca: make(map[string]caResult), picked: make(map[int]bool), aliases: make(map[string]string), ascii: os.Getenv("EASY_SSO_ASCII") != "" || os.Getenv("TERM") == "dumb", noColor: noColor || os.Getenv("TERM") == "dumb", dark: os.Getenv("EASY_SSO_THEME") == "dark"}
	if os.Getenv("EASY_SSO_THEME") == "" {
		m.dark = s.Theme == "dark"
	}
	for k, v := range s.AccountAliases {
		m.aliases[k] = v
	}
	for i, p := range s.Profiles {
		if p.Name == "default" {
			m.selectedProfile = i
			break
		}
	}
	if len(s.Profiles) == 0 {
		m.selectedProfile = -1
		m.status = "No configured profiles. Bookmark browsing remains available."
	}
	return m
}
func orderedProfiles(profiles []domain.Profile) []domain.Profile {
	profiles = append([]domain.Profile{}, profiles...)
	for i := range profiles {
		profiles[i].Chain = append([]string(nil), profiles[i].Chain...)
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		a, b := profiles[i], profiles[j]
		if a.SSOSession != b.SSOSession {
			return a.SSOSession < b.SSOSession
		}
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.Name < b.Name
	})
	return profiles
}
func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.SetWindowTitle("easy-sso"), m.load(), m.refreshProfiles(), tick(), animate())
}
func tick() tea.Cmd { return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m *Model) profile() (domain.Profile, bool) {
	if m.selectedProfile < 0 || m.selectedProfile >= len(m.services.Profiles) {
		return domain.Profile{}, false
	}
	return m.services.Profiles[m.selectedProfile], true
}
func (m *Model) load() tea.Cmd {
	e := m.epoch
	s := m.services
	return func() tea.Msg {
		var msg loadedMsg
		msg.epoch = e
		if s.Connections != nil {
			var err error
			msg.connections, err = s.Connections.List()
			if err != nil {
				msg.err = err
			}
		}
		if s.Credentials != nil {
			var err error
			msg.roles, err = s.Credentials.List()
			if err != nil {
				msg.err = err
			}
		}
		if s.Sessions != nil {
			if recovery, ok := s.Sessions.(interface{ Reconcile(context.Context) }); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				recovery.Reconcile(ctx)
				cancel()
			}
			msg.runs = s.Sessions.List()
		}
		return msg
	}
}
func (m *Model) operation(action string, f func(context.Context) (domain.Run, domain.ImportPlan, error)) tea.Cmd {
	m.authNotice, m.authURL, m.authFeedback = "", "", ""
	m.request++
	id, e := m.request, m.epoch
	m.busy = true
	m.operationButton = ""
	m.operationStarted = time.Now()
	m.status = action + " in progress..."
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	m.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		r, p, err := f(ctx)
		return resultMsg{epoch: e, request: id, action: action, run: r, plan: p, err: err}
	}
}
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case timer.TickMsg:
		return m, m.updateExpiryTimer(x)
	case animationMsg:
		if m.busy {
			if m.activitySpinner.Spinner.FPS == 0 {
				m.activitySpinner = spinner.New(spinner.WithSpinner(spinner.Line))
			}
			m.activitySpinner, _ = m.activitySpinner.Update(spinner.TickMsg{Time: time.Time(x), ID: m.activitySpinner.ID()})
		}
		return m, animate()
	case themeSavedMsg:
		m.themeSaving = false
		if m.themePending != "" {
			theme := m.themePending
			m.themePending = ""
			return m, m.persistTheme(theme)
		}
		if x.err != nil {
			m.status = "Theme changed for this session; could not save preference: " + x.err.Error()
		}
		return m, nil
	case tea.MouseMsg:
		return m, m.mouse(x)
	case AuthNoticeMsg:
		m.receiveAuthNotice(string(x))
	case clipboardMsg:
		m.status = x.feedback
		return m, nil
	case caMsg:
		return m, m.receiveCA(x)
	case caExportMsg:
		return m, m.receiveExport(x)
	case caCopyMsg:
		// C runs its own check; an automatic one may still be in flight.
		r := m.ca[x.path]
		r.ready, r.stale, r.err = x.err == nil, x.stale, ""
		if x.err != nil {
			r.err = x.err.Error()
		}
		m.ca[x.path] = r
		m.status = x.feedback
		if x.err != nil {
			return m, nil
		}
		return m, m.refreshExport(x.path)
	case authActionMsg:
		if m.busy && x.request == m.request && x.url == m.authURL {
			m.authFeedback = x.feedback
		}
	case tea.WindowSizeMsg:
		m.width = x.Width
		m.height = x.Height
	case tickMsg:
		return m, tea.Batch(m.load(), m.refreshProfiles(), tick())
	case profilesMsg:
		if x.request != m.profilesRequest {
			return m, nil
		}
		m.profilesLoading = false
		if m.busy {
			m.pendingProfiles = &x
			return m, nil
		}
		if m.applyProfiles(x) {
			return m, m.load()
		}
	case loadedMsg:
		if x.epoch != m.epoch {
			return m, m.load()
		}
		selectedRun := ""
		if rs := m.visibleRuns(); m.selectedRow >= 0 && m.selectedRow < len(rs) {
			selectedRun = rs[m.selectedRow].ID
		}
		m.connections = x.connections
		m.roles = x.roles
		m.applyProfiles(profilesMsg{profiles: m.services.Profiles})
		m.runs = x.runs
		if selectedRun != "" {
			for i, run := range m.visibleRuns() {
				if run.ID == selectedRun {
					m.selectedRow = i
					break
				}
			}
		}
		if m.review != nil && m.review.kind == "run" {
			for _, run := range m.runs {
				if run.ID == m.review.run.ID {
					m.review.run = run
					break
				}
			}
		}
		for i, tab := range m.terminalTabs {
			for _, r := range m.runs {
				if r.ID == tab.ID {
					m.terminalTabs[i] = r
					break
				}
			}
		}
		if m.pendingTarget != "" {
			for i, c := range m.targets() {
				if c.ID == m.pendingTarget {
					m.selectedRow = i
					break
				}
			}
			m.pendingTarget = ""
		}
		if m.pendingRoute != nil {
			// Prefer the opened bookmark's ID; a first open has none yet, so
			// fall back to its route.
			found := false
			for i, c := range m.targets() {
				if m.pendingRoute.ID != "" && c.ID == m.pendingRoute.ID {
					m.selectedRow, found = i, true
					break
				}
			}
			for i, c := range m.targets() {
				if !found && sameDocumentDBRoute(c, *m.pendingRoute) {
					m.selectedRow, found = i, true
				}
			}
			m.pendingRoute = nil
		}
		if x.err != nil {
			m.status = "Error: " + x.err.Error()
		}
		m.clampRow()
		if m.exitAfterOperation && !m.busy {
			m.exitAfterOperation = false
			return m, m.requestExit()
		}
		return m, m.syncExpiryTimers(time.Now())
	case resultMsg:
		if x.request != m.request {
			return m, nil
		}
		m.busy = false
		m.authNotice, m.authURL, m.authFeedback = "", "", ""
		m.cancel = nil
		if m.pendingProfiles != nil {
			m.applyProfiles(*m.pendingProfiles)
			m.pendingProfiles = nil
		}
		if x.err == nil && x.action == "Save account label" {
			m.aliases[x.aliasID] = x.aliasLabel
		}
		if m.exitAfterOperation {
			m.form = nil
			m.picker = false
			return m, m.afterOperation()
		}
		if x.action == "Exit" && x.err == nil {
			return m, tea.Quit
		}
		if x.action == "Reconnect terminal" {
			from := m.reconnectFrom
			m.reconnectFrom = ""
			if x.err != nil {
				m.status = "Error: " + x.err.Error()
				// A replacement that launched a screen before failing stays
				// visible with its diagnostics, like a failed first connection.
				if x.run.ID != "" && m.services.Terminals != nil && m.services.Terminals.Has(x.run.ID) {
					m.showTerminal(x.run)
				}
			} else {
				m.replaceTerminalTab(from, x.run)
				m.status = "Terminal reconnected."
			}
			return m, m.afterOperation()
		}
		if x.action == "Close terminal" {
			if m.services.Terminals == nil || !m.services.Terminals.Has(x.run.ID) {
				m.removeTerminalTab(x.run.ID)
			}
			if x.err != nil {
				m.status = "Error: " + x.err.Error()
			} else {
				m.status = "Session closed."
			}
			return m, m.afterOperation()
		}
		if x.epoch != m.epoch {
			return m, m.afterOperation()
		}
		if x.action == "Open connection" && (x.run.Connection.Kind == "ec2" || x.run.Connection.IAMDatabase()) && m.services.Terminals != nil && m.services.Terminals.Has(x.run.ID) {
			m.showTerminal(x.run)
		}
		if x.err != nil {
			m.status = "Error: " + x.err.Error()
			return m, m.afterOperation()
		}
		switch x.action {
		case "Import preview":
			m.form = nil
			m.review = &review{kind: "import", plan: x.plan}
			m.status = "Review additions and conflicts. Existing records are kept by default."
		case "Exit":
			return m, tea.Quit
		case "Activate roles":
			m.review = nil
			m.form = nil
			m.status = "AWS CLI SSO cleanup complete. Selected role credentials published."
		case "Retry cleanup":
			m.status = "AWS CLI SSO cleanup complete. Activate roles again to publish a new batch."
		case "Unpublish credentials":
			m.review = nil
			m.status = "Credentials unpublished: the profile entry is out of the AWS credentials file until you republish it (u). Its keys stay only in this app's memory."
		case "Republish credentials":
			m.review = nil
			m.status = "Credentials republished: the profile entry is back in the AWS credentials file."
		case "Open connection":
			if (x.run.Connection.Kind == "ec2" || x.run.Connection.IAMDatabase()) && m.services.Terminals != nil && m.services.Terminals.Has(x.run.ID) {
				m.pendingTarget = x.run.Connection.ID
			}
			m.review = nil
			m.form = nil
			if strings.EqualFold(x.run.State, "listening") && isDocumentDB(x.run.Connection) {
				m.status = fmt.Sprintf("Tunnel open at 127.0.0.1:%d · profile [%s]. Connect DataGrip or any MongoDB client with your DocumentDB user; c copies the URL, v shows steps.", x.run.Connection.LocalPort, x.run.Profile.Name)
				m.caStatus, m.caStatusPort = m.status, x.run.Connection.LocalPort
				m.caStatusPath, _ = m.caPath(x.run.Connection)
				route := x.run.Connection
				m.pendingRoute = &route
				return m, tea.Batch(m.afterOperation(), m.ensureCA(x.run.Connection))
			} else if strings.EqualFold(x.run.State, "listening") {
				m.status = fmt.Sprintf("Tunnel open at localhost:%d · profile [%s]; database login not attempted. Set up IAM login (l) for automatic token login, or use your database client.", x.run.Connection.LocalPort, x.run.Profile.Name)
			} else {
				m.status = "Session state: " + x.run.State + ". The separate terminal window keeps its original identity."
				if m.services.Terminals != nil && m.services.Terminals.Has(x.run.ID) {
					m.status = "Terminal connected. Ctrl+] returns to the AWS workspace."
				}
			}
		default:
			m.review = nil
			m.form = nil
			m.status = x.action + " complete."
		}
		return m, m.afterOperation()
	case tea.KeyMsg:
		return m, m.key(x)
	}
	return m, nil
}
func (m *Model) changeProfile(n int) {
	m.terminalFocus = false
	if n < 0 || n >= len(m.services.Profiles) || n == m.selectedProfile {
		return
	}
	m.selectedProfile = n
	m.sidebarScroll = 0
	if m.expanded == nil {
		m.expanded = map[string]bool{}
	}
	p := m.services.Profiles[n]
	m.expanded[accountKey(p)] = true
	m.selectedRow = -1
	m.review = nil
	m.form = nil
	m.picker = false
	m.picked = make(map[int]bool)
	m.drafts = make(map[screen]form)
	m.scroll = 0
	m.epoch++
	m.status = "Workspace context changed. Existing sessions and tunnels retain their identities."
}
func (m *Model) changeScreen(s screen) {
	m.terminalFocus = false
	if s == m.screen {
		return
	}
	m.saveDraft()
	m.screen = s
	m.form = nil
	m.review = nil
	m.picker = false
	m.selectedRow = 0
	m.scroll = 0
	m.epoch++
}
func (m *Model) saveDraft() {
	if m.form != nil && m.form.original == nil && (m.form.kind == "ec2" || m.form.kind == "db") {
		f := *m.form
		f.fields = append([]field(nil), f.fields...)
		m.drafts[m.screen] = f
	}
}
func (m *Model) rows() int {
	switch m.screen {
	case ec2, databases:
		return len(m.targets())
	case sessions, tunnels:
		return len(m.visibleRuns())
	case credentials:
		return len(m.roles)
	}
	return 0
}
func (m *Model) clampRow() {
	if m.selectedRow == -1 {
		return
	} // Explicitly cleared by a profile change.
	n := m.rows()
	if m.selectedRow >= n {
		m.selectedRow = max(0, n-1)
	}
	if m.selectedRow < -1 {
		m.selectedRow = 0
	}
}
func (m *Model) targets() []domain.Connection {
	p, ok := m.profile()
	if !ok {
		return nil
	}
	kind := "ec2"
	if m.screen == databases {
		kind = "db"
	}
	var out []domain.Connection
	for _, c := range m.connections {
		if c.Kind == kind && c.AccountID == p.AccountID && c.Partition == p.Partition {
			out = append(out, c)
		}
	}
	return out
}
func (m *Model) visibleRuns() []domain.Run {
	var out []domain.Run
	for _, r := range m.runs {
		if (m.screen == sessions && r.Connection.Kind == "ec2") || (m.screen == tunnels && r.Connection.Kind == "db") {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if m.screen == sessions {
		if len(out) > 50 {
			out = out[:50]
		}
	}
	return out
}
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
}
