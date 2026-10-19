package ui

import (
	"context"
	"errors"
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"reflect"
)

func (m *Model) open(c domain.Connection) tea.Cmd { return m.openMode(c, false) }
func (m *Model) openMode(c domain.Connection, window bool) tea.Cmd {
	// A bookmark saved before this check existed must not reach a session.
	if err := connections.DocumentDBMode(c); err != nil {
		m.status = err.Error()
		return nil
	}
	p, ok := m.profile()
	if !ok || p.Error != "" {
		m.status = "Select an available profile first."
		return nil
	}
	if !m.services.Prerequisites.Ready || m.services.Sessions == nil || m.services.Credentials == nil {
		m.status = "Connection blocked: external prerequisites unavailable. Bookmark browsing remains available."
		return nil
	}
	var alias string
	for _, r := range m.roles {
		if sameProfile(r.Profile, p) && roleActive(r) {
			alias = r.Alias
			break
		}
	}
	if alias == "" {
		m.status = fmt.Sprintf("Warning: Selected account %s · profile [%s]. Log in with Ctrl+A before connecting.", p.AccountID, p.Name)
		return nil
	}
	if c.AccountID != p.AccountID || c.Partition != p.Partition {
		m.status = "Connection blocked: bookmark belongs to another account or partition."
		return nil
	}
	loadProfiles := m.services.LoadProfiles
	cmd := m.operation("Open connection", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
		if loadProfiles != nil {
			current, err := loadProfiles()
			if err != nil {
				return domain.Run{}, domain.ImportPlan{}, fmt.Errorf("AWS config reload failed: %w", err)
			}
			matches := false
			for _, configured := range current {
				if configured.Name == p.Name && reflect.DeepEqual(configured, p) {
					matches = true
					break
				}
			}
			if !matches {
				return domain.Run{}, domain.ImportPlan{}, errors.New("Connection blocked: AWS profile changed after review. Reload profiles and review the current identity.")
			}
		}
		role, snapshot, err := m.services.Credentials.Snapshot(alias)
		if err != nil {
			return domain.Run{}, domain.ImportPlan{}, err
		}
		if !sameProfile(role.Profile, p) {
			return domain.Run{}, domain.ImportPlan{}, errors.New("Connection blocked: the resolved AWS identity does not match this account and role.")
		}
		open := m.services.Sessions.Open
		if window {
			if m.services.OpenWindow == nil {
				return domain.Run{}, domain.ImportPlan{}, errors.New("separate terminal windows are unavailable on this host")
			}
			open = m.services.OpenWindow
		}
		run, err := open(ctx, c, role, snapshot)
		return run, domain.ImportPlan{}, err
	})
	if (c.Kind == "ec2" || c.IAMDatabase()) && !window {
		m.operationButton = "enter"
	}
	return cmd
}
func (m *Model) focusSelected() tea.Cmd {
	rs := m.visibleRuns()
	if m.selectedRow >= 0 && len(rs) > m.selectedRow {
		return m.focusRun(rs[m.selectedRow])
	}
	return nil
}
func (m *Model) canFocusRun(r domain.Run) bool {
	if m.services.Terminals != nil {
		return m.services.Terminals.Has(r.ID)
	}
	return activeRun(r) && m.services.FocusTerminal && r.Connection.Kind == "ec2"
}

func (m *Model) focusRun(r domain.Run) tea.Cmd {
	if m.services.Terminals != nil && m.services.Terminals.Has(r.ID) {
		m.showTerminal(r)
		m.review = nil
		return nil
	}

	if m.services.Terminals != nil || !m.services.FocusTerminal || r.Connection.Kind == "db" {
		m.status = m.terminalHint(r)
		return nil
	}
	if m.services.Sessions == nil {
		return nil
	}
	return m.operation("Focus terminal", func(context.Context) (domain.Run, domain.ImportPlan, error) {
		return domain.Run{}, domain.ImportPlan{}, m.services.Sessions.Focus(r.ID)
	})
}
func (m *Model) terminalHint(r domain.Run) string {
	if m.services.Terminals != nil && m.services.Terminals.Has(r.ID) {
		return "f: Focus terminal | Ctrl+]: AWS workspace | " + safe(r.Title)
	}

	if r.Connection.Kind == "db" && r.Connection.IAMDatabase() {
		return "No local database terminal. In Databases, Connect terminal starts or reuses a tunnel and signs in."
	}
	if r.Connection.Kind == "db" {
		return "Tunnel transport only; database authentication is handled by your client."
	}
	if m.services.Terminals != nil {
		return "This shell belongs to another workspace or external terminal. In EC2, Connect terminal opens a shell here."
	}
	if m.services.FocusTerminal {
		return "f: Focus terminal | " + safe(r.Title)
	}
	return "Switch to the separate terminal window: " + safe(r.Title)
}
func (m *Model) stopSelected() tea.Cmd {
	rs := m.visibleRuns()
	if m.selectedRow >= 0 && len(rs) > m.selectedRow {
		m.review = &review{kind: "stop", run: rs[m.selectedRow], listScroll: m.scroll}
		m.scroll = 0
	}
	return nil
}
func (m *Model) recheckSelected() tea.Cmd {
	rs := m.visibleRuns()
	if m.selectedRow >= 0 && len(rs) > m.selectedRow {
		return m.recheckRun(rs[m.selectedRow])
	}
	return nil
}
func (m *Model) recheckRun(r domain.Run) tea.Cmd {
	if m.services.Sessions == nil {
		return nil
	}
	return m.operation("Recheck status", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
		return domain.Run{}, domain.ImportPlan{}, m.services.Sessions.Recheck(ctx, r.ID)
	})
}

// Saved EC2 targets already carry the full identity and route visible in the
// workspace. Connect directly, retaining the backend identity validation.
func (m *Model) connectSelected() tea.Cmd {
	targets := m.targets()
	if m.selectedRow < 0 || m.selectedRow >= len(targets) {
		return nil
	}
	c := targets[m.selectedRow]
	p, ok := m.profile()
	if !ok {
		return nil
	}
	for i := len(m.runs) - 1; i >= 0; i-- {
		r := m.runs[i]
		// Recovered session metadata does not transfer ownership of its PTY.
		// Only reuse a terminal this workspace can actually display.
		if m.services.Terminals != nil && !m.services.Terminals.Has(r.ID) {
			continue
		}
		if activeRun(r) && r.Connection.Kind == "ec2" && r.Connection.InstanceID == c.InstanceID && r.Connection.Region == c.Region && r.Connection.DocumentName == c.DocumentName && sameProfile(r.Profile, p) {
			return m.focusRun(r)
		}
	}
	return m.open(c)
}
