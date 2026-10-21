package ui

import (
	"context"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"path/filepath"
)

func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if m.review != nil && m.review.kind == "exit" {
		if m.busy {
			return nil
		}
		return m.reviewKey(k)
	}
	if !k.Paste {
		switch key {
		case "ctrl+]":
			m.selectTab(0)
			return nil
		case "shift+tab":
			m.cycleTab(1)
			return nil
		case "ctrl+w", "alt+w":
			if m.terminalID != "" {
				return m.closeTerminalTab()
			}
		}
	}
	if run, ok := m.embeddedRun(); ok {
		if !k.Paste && (key == "alt+pgup" || key == "alt+pgdown") {
			lines := max(1, m.terminalArea.height-1)
			if key == "alt+pgdown" {
				lines = -lines
			}
			return m.scrollTerminal(lines, false, 0, 0)
		}
		if run.Connection.Kind == "ec2" && m.terminalEnded(run) {
			if m.reconnecting(run) {
				if key == "esc" && m.cancel != nil {
					m.cancel()
					m.status = "Cancellation requested. Waiting for cleanup and final state."
				}
				return nil
			}
			if !k.Paste && (key == "r" || key == "enter") {
				return m.reconnectTerminal()
			}
			m.status = "Session ended. r: reconnect, Ctrl+W: close, Ctrl+]: AWS workspace."
			return nil
		}
		text := ""
		if k.Type == tea.KeyRunes {
			text = string(k.Runes)
		}
		if err := m.services.Terminals.Send(run.ID, key, text, k.Paste); err != nil {
			m.status = err.Error()
		}
		return nil
	}
	if key == "ctrl+t" {
		return m.toggleTheme()
	}
	if key == "pgdown" {
		m.scroll += 5
		return nil
	}
	if key == "pgup" {
		m.scroll = max(0, m.scroll-5)
		return nil
	}
	if m.busy {
		if m.authURL != "" && (key == "c" || key == "o") {
			return m.authAction(key)
		}
		if (key == "q" || key == "ctrl+c") && m.cancel != nil {
			m.exitAfterOperation = true
			m.cancel()
			m.status = "Exit requested. Waiting for operation cleanup before exit review."
			return nil
		}
		if key == "esc" && m.cancel != nil {
			m.cancel()
			m.status = "Cancellation requested. Waiting for cleanup and final state."
		}
		return nil
	}
	if key == "ctrl+c" {
		m.saveDraft()
		m.form = nil
		m.picker = false
		return m.requestExit()
	}
	if key == "ctrl+r" {
		return m.refreshProfiles()
	}
	if m.form != nil {
		return m.formKey(k)
	}
	if key == "?" {
		m.help = !m.help
		m.scroll = 0
		return nil
	}
	if m.help && key == "esc" {
		m.help = false
		return nil
	}
	if m.help {
		return nil
	}
	if m.review != nil {
		return m.reviewKey(k)
	}
	if m.picker {
		switch key {
		case "esc":
			m.picker = false
		case "up", "k":
			if m.pickerRow > 0 {
				m.pickerRow--
			}
		case "down", "j":
			if m.pickerRow+1 < len(m.services.Profiles) {
				m.pickerRow++
			}
		case " ":
			p := m.services.Profiles[m.pickerRow]
			if p.Error == "" {
				m.picked[m.pickerRow] = !m.picked[m.pickerRow]
			}
		case "enter":
			m.activationReview()
		}
		return nil
	}
	switch key {
	case "ctrl+left", "ctrl+right":
		step := 1
		if key == "ctrl+left" {
			step = -1
		}
		m.changeScreen(screen((int(m.screen) + step + len(screenNames)) % len(screenNames)))
		m.focus = 1
		return m.load()
	case "tab":
		if m.focus == 0 {
			m.changeScreen(ec2)
			m.focus = 1
		} else if m.screen == ec2 {
			m.changeScreen(databases)
		} else {
			m.focus = 0
		}
		return m.load()
	case "1", "2", "3", "4", "5", "6":
		m.changeScreen(screen(int(key[0] - '1')))
		m.focus = 1
		return m.load()
	case "s":
		m.changeScreen(sessions)
		return m.load()
	case "q":
		return m.requestExit()
	case "esc":
		m.help = false
		m.focus = 1
	case "up", "k":
		m.scroll = 0
		if m.focus == 0 {
			m.changeProfile(m.selectedProfile - 1)
			return m.load()
		}
		if m.selectedRow > 0 {
			m.selectedRow--
		}
	case "down", "j":
		m.scroll = 0
		if m.focus == 0 {
			m.changeProfile(m.selectedProfile + 1)
			return m.load()
		}
		if m.selectedRow+1 < m.rows() {
			m.selectedRow++
		}
	case "enter":
		if m.focus == 0 {
			m.focus = 1
			return nil
		}
		return m.reviewSelected()
	case "w":
		if m.screen == ec2 {
			ts := m.targets()
			if m.selectedRow >= 0 && m.selectedRow < len(ts) {
				return m.openMode(ts[m.selectedRow], true)
			}
		}
	case "v":
		return m.detailsSelected()
	case "n":
		if m.screen == ec2 || m.screen == databases {
			m.newForm()
		}
	case "ctrl+a":
		if m.selectedProfileActive() {
			p, _ := m.profile()
			var selections []domain.Selection
			for _, r := range m.roles {
				if sameProfile(r.Profile, p) {
					selections = append(selections, domain.Selection{Profile: p, Alias: r.Alias})
				}
			}
			m.review = &review{kind: "logout", selections: selections}
			return nil
		}
		return m.loginSelected()
	case "A":
		m.startActivation()
	case "y":
		if p, ok := m.profile(); ok {
			m.form = &form{kind: "alias", fields: []field{{label: "Account label", value: m.accountAlias(p.AccountID), id: "label"}}}
		}
	case "u":
		if m.screen == credentials && m.selectedRow >= 0 && m.selectedRow < len(m.roles) {
			return m.togglePublication(m.roles[m.selectedRow])
		}
	case "f":
		if m.screen == sessions || m.screen == tunnels {
			return m.focusSelected()
		}
	case "l":
		if m.screen == databases {
			ts := m.targets()
			if m.selectedRow >= 0 && m.selectedRow < len(ts) {
				m.setupDatabaseLogin(ts[m.selectedRow])
			}
		}
	case "d":
		if m.screen == sessions || m.screen == tunnels {
			return m.stopSelected()
		}
		if m.screen == credentials && m.selectedRow >= 0 && len(m.roles) > m.selectedRow {
			m.review = &review{kind: "remove", role: m.roles[m.selectedRow]}
		}
	case "r":
		if m.screen == credentials && m.selectedRow >= 0 && len(m.roles) > m.selectedRow {
			r := m.roles[m.selectedRow]
			m.renewRole(r)
		} else if m.screen == sessions || m.screen == tunnels {
			return m.recheckSelected()
		}
	case "c", "C":
		if key == "c" && m.screen == credentials && m.selectedRow >= 0 && m.selectedRow < len(m.roles) {
			return m.copyProfileName(m.roles[m.selectedRow])
		}
		if cmd, ok := m.copyDocumentDBKey(key); ok {
			return cmd
		}
	case "x":
		if cmd, ok := m.exportCAKey(key); ok {
			return cmd
		}
	case "g":
		return m.launchKey()
	case "R":
		if m.screen == credentials && m.services.Credentials != nil {
			return m.operation("Retry cleanup", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
				return domain.Run{}, domain.ImportPlan{}, m.services.Credentials.RetryCleanup(ctx)
			})
		}
	case "e", "i":
		if key == "e" && (m.screen == ec2 || m.screen == databases) {
			ts := m.targets()
			if m.selectedRow >= 0 && m.selectedRow < len(ts) {
				m.editConnection(ts[m.selectedRow])
			}
			return nil
		}
		if m.screen == backup {
			kind := "export"
			if key == "i" {
				kind = "import"
			}
			m.form = &form{kind: kind, fields: []field{{label: "File path", value: filepath.Join(m.services.Prerequisites.StateDir, "bookmarks-backup.json"), id: "path"}}}
		}
	}
	return nil
}
