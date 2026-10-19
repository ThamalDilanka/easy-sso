package ui

import (
	"context"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"strconv"
	"strings"
)

// Enter performs the everyday action; details always have their own shortcut.
func (m *Model) reviewSelected() tea.Cmd {
	switch m.screen {
	case ec2:
		return m.connectSelected()
	case databases:
		ts := m.targets()
		if m.selectedRow >= 0 && m.selectedRow < len(ts) {
			return m.open(ts[m.selectedRow])
		}
	case sessions, tunnels:
		rs := m.visibleRuns()
		if m.selectedRow >= 0 && m.selectedRow < len(rs) && m.canFocusRun(rs[m.selectedRow]) {
			return m.focusRun(rs[m.selectedRow])
		}
		return m.detailsSelected()
	case credentials:
		return m.detailsSelected()
	}
	return nil
}

func (m *Model) detailsSelected() tea.Cmd {
	switch m.screen {
	case ec2, databases:
		ts := m.targets()
		if m.selectedRow >= 0 && m.selectedRow < len(ts) {
			m.connectionReview(ts[m.selectedRow])
			m.review.details = true
		}
	case sessions, tunnels:
		rs := m.visibleRuns()
		if m.selectedRow >= 0 && m.selectedRow < len(rs) {
			r := rs[m.selectedRow]
			m.review = &review{kind: "run", run: r, listScroll: m.scroll}
			m.scroll = 0
			if isDocumentDB(r.Connection) {
				if !activeTunnel(r) {
					m.status = "Tunnel not open; Enter on the bookmark reopens it."
					return nil
				}
				return m.ensureCA(r.Connection)
			}
		}
	case credentials:
		if m.selectedRow >= 0 && m.selectedRow < len(m.roles) {
			m.review = &review{kind: "role", role: m.roles[m.selectedRow], listScroll: m.scroll}
			m.scroll = 0
		}
	}
	return nil
}
func activeRun(r domain.Run) bool {
	switch strings.ToLower(r.State) {
	case "ended", "stopped", "failed":
		return false
	}
	return true
}
func (m *Model) connectionReview(c domain.Connection) {
	p, _ := m.profile()
	r := &review{kind: "connection", connection: c, listScroll: m.scroll}
	m.scroll = 0
	for _, run := range m.runs {
		if activeRun(run) && run.Connection.Kind == c.Kind && run.Connection.AccountID == c.AccountID && run.Connection.Region == c.Region && run.Connection.InstanceID == c.InstanceID && run.Connection.DocumentName == c.DocumentName && sameProfile(run.Profile, p) && c.Kind == "ec2" {
			r.duplicate = true
			r.run = run
			break
		}
	}
	m.review = r
}
func (m *Model) reviewKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+t" {
		return m.toggleTheme()
	}
	r := m.review
	if key == "esc" {
		m.scroll = r.listScroll
		m.review = nil
		return nil
	}
	switch r.kind {
	case "connection":
		if cmd, ok := m.copyDocumentDBKey(key); ok {
			return cmd
		}
		if cmd, ok := m.exportCAKey(key); ok {
			return cmd
		}
		if key == "w" && r.connection.Kind == "ec2" {
			return m.openMode(r.connection, true)
		}
		if key == "l" && r.connection.Kind == "db" {
			m.setupDatabaseLogin(r.connection)
			return nil
		}
		if key == "e" {
			m.editConnection(r.connection)
			return nil
		}
		if key == "p" && r.connection.Kind == "db" {
			m.form = &form{kind: "port", fields: []field{{"Local port", strconv.Itoa(r.connection.LocalPort)}}}
			return nil
		}
		if key == "f" && r.duplicate {
			return m.focusRun(r.run)
		}
		if key == "N" && r.duplicate {
			r.duplicate = false
			m.status = "Explicit new shell selected. Enter confirms this additional session."
			return nil
		}
		if key == "enter" {
			if r.duplicate && m.canFocusRun(r.run) {
				return m.focusRun(r.run)
			}
			if r.duplicate {
				m.status = "Shell already open. Switch using terminal tabs; N explicitly requests a new shell."
				return nil
			}
			return m.open(r.connection)
		}
	case "run":
		if cmd, ok := m.copyDocumentDBKey(key); ok {
			return cmd
		}
		if cmd, ok := m.exportCAKey(key); ok {
			return cmd
		}
		if key == "f" || key == "enter" {
			return m.focusRun(r.run)
		}
		if key == "d" {
			r.kind = "stop"
			m.scroll = 0
		}
		if key == "r" {
			return m.recheckRun(r.run)
		}
	case "role":
		if key == "d" {
			r.kind = "remove"
		}
		if key == "r" {
			m.renewRole(r.role)
		}
		if key == "u" {
			return m.togglePublication(r.role)
		}
		if key == "c" {
			return m.copyProfileName(r.role)
		}
	case "stop":
		if key == "enter" && m.services.Sessions != nil {
			id := r.run.ID
			return m.operation("Stop selected session", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
				return domain.Run{}, domain.ImportPlan{}, m.services.Sessions.Stop(ctx, id)
			})
		}
	case "logout":
		if key == "enter" && m.services.Credentials != nil {
			selections := append([]domain.Selection(nil), r.selections...)
			return m.operation("Log out", func(context.Context) (domain.Run, domain.ImportPlan, error) {
				for _, sel := range selections {
					if err := m.services.Credentials.Remove(sel.Alias); err != nil {
						return domain.Run{}, domain.ImportPlan{}, err
					}
				}
				return domain.Run{}, domain.ImportPlan{}, nil
			})
		}
	case "remove":
		if key == "enter" && m.services.Credentials != nil {
			alias := r.role.Alias
			return m.operation("Remove managed role", func(context.Context) (domain.Run, domain.ImportPlan, error) {
				return domain.Run{}, domain.ImportPlan{}, m.services.Credentials.Remove(alias)
			})
		}
	case "import":
		if key == " " {
			r.replace = !r.replace
		}
		if key == "enter" && m.services.Connections != nil {
			p, replace := r.plan, r.replace
			return m.operation("Import bookmarks", func(context.Context) (domain.Run, domain.ImportPlan, error) {
				return domain.Run{}, domain.ImportPlan{}, m.services.Connections.Commit(p, replace)
			})
		}
	case "exit":
		if key == "c" {
			return m.exitWithCredentials(true)
		}
		if key == "enter" {
			return m.exitWorkspace()
		}
	}
	return nil
}
