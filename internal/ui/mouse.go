package ui

import (
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

// Hit regions are rebuilt from the same wrapped/scrolled cells that are drawn.
// No terminal coordinates are guessed from profile or resource-name lengths.
type mouseAction struct {
	kind, key string
	index     int
}
type hitRegion struct {
	x, y, width int
	action      mouseAction
}

func (m *Model) hit(x, y, width int, a mouseAction) {
	if width > 0 && a.kind != "" {
		m.hits = append(m.hits, hitRegion{x, y, width, a})
	}
}
func (m *Model) clickable(line string, a mouseAction) string {
	if m.rowActions == nil {
		m.rowActions = map[string][]mouseAction{}
	}
	m.rowActions[line] = append(m.rowActions[line], a)
	return line
}
func accountKey(p domain.Profile) string { return p.SSOSession + "/" + p.AccountID }
func (m *Model) accountOpen(p domain.Profile) bool {
	if value, ok := m.expanded[accountKey(p)]; ok {
		return value
	}
	selected, ok := m.profile()
	return ok && accountKey(selected) == accountKey(p)
}
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	// Only presses dispatch; releases and drag motion must not double-submit.
	if msg.Action != tea.MouseActionPress {
		return nil
	}
	if msg.Button == tea.MouseButtonWheelDown || msg.Button == tea.MouseButtonWheelUp {
		if m.busy {
			return nil
		}
		if m.terminalID != "" && m.terminalArea.contains(msg.X, msg.Y) {
			lines := -1
			if msg.Button == tea.MouseButtonWheelUp {
				lines = 1
			}
			return m.scrollTerminal(lines, true, msg.X-m.terminalArea.x, msg.Y-m.terminalArea.y)
		}
		delta := 3
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -3
		}
		side := 0
		if m.width >= 100 {
			side = 28
			if m.width >= 120 {
				side = 32
			}
		}
		if (side > 0 && msg.X < side) || (side == 0 && m.focus == 0 && m.form == nil && m.review == nil && !m.help && !m.picker) {
			m.sidebarScroll = min(m.maxSidebarScroll, max(0, m.sidebarScroll+delta))
		} else {
			m.scroll = min(m.maxScroll, max(0, m.scroll+delta))
		}
		return nil
	}
	if msg.Button != tea.MouseButtonLeft {
		return nil
	}
	for _, h := range m.hits {
		if msg.Y != h.y || msg.X < h.x || msg.X >= h.x+h.width {
			continue
		}
		a := h.action
		switch a.kind {
		case "tab":
			m.selectTab(a.index)
			return nil
		case "tab-close":
			if m.busy {
				return nil
			}
			m.selectTab(a.index)
			return m.closeTerminalTab()
		case "tab-prev":
			m.cycleTab(-1)
			return nil
		case "tab-next":
			m.cycleTab(1)
			return nil
		}
		if a.kind == "theme" {
			return m.toggleTheme()
		}
		if m.busy && !(a.kind == "key" && (a.key == "esc" || (m.authURL != "" && (a.key == "o" || a.key == "c")))) {
			return nil
		}
		if m.help && !(a.kind == "key" && (a.key == "esc" || a.key == "?")) {
			return nil
		}
		switch a.kind {
		case "screen":
			m.changeScreen(screen(a.index))
			m.focus = 1
			return m.load()
		case "profile":
			m.terminalFocus = false
			m.changeProfile(a.index)
			m.focus = 0
			if m.width < 100 {
				m.focus = 1
			}
			return m.load()
		case "account":
			m.terminalFocus = false
			if a.index < 0 || a.index >= len(m.services.Profiles) {
				return nil
			}
			p := m.services.Profiles[a.index]
			open := m.accountOpen(p)
			if m.expanded == nil {
				m.expanded = map[string]bool{}
			}
			m.expanded[accountKey(p)] = !open
			m.focus = 0
		case "row":
			m.terminalFocus = false
			m.selectedRow = a.index
			m.focus = 1
		case "field":
			if m.form != nil {
				m.form.index = a.index
			}
		case "ack":
			if m.form != nil && m.review != nil {
				m.form.index = len(m.form.fields)
				m.review.acknowledged = !m.review.acknowledged
			}
		case "pick":
			if m.picker && a.index < len(m.services.Profiles) && m.services.Profiles[a.index].Error == "" {
				m.pickerRow = a.index
				m.picked[a.index] = !m.picked[a.index]
			}
		case "terminal":
			m.terminalFocus = true
		case "accounts":
			m.focus = 1 - m.focus
		case "key":
			m.terminalFocus = false
			m.focus = 1
			return m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(a.key)})
		}
		return nil
	}
	return nil
}

type control struct{ label, key string }

// pauseControl offers Pause for an active role and Resume for a paused one;
// expired, missing or unknown roles get neither, since only Renew helps.
func pauseControl(r domain.ManagedRole) (control, bool) {
	if rolePaused(r) {
		return control{"Resume", "p"}, true
	}
	if roleActive(r) {
		return control{"Pause", "p"}, true
	}
	return control{}, false
}

func (m *Model) controls() []control {
	if m.busy && m.authURL != "" {
		return []control{{"Open browser", "o"}, {"Copy URL", "c"}, {"Cancel", "esc"}}
	}
	if m.busy {
		if m.operationButton == "enter" {
			return []control{{"Connect terminal", "enter"}, {"Cancel", "esc"}}
		}
		return []control{{"Cancel", "esc"}}
	}
	if m.help {
		return []control{{"Close help", "esc"}}
	}
	if m.form != nil {
		if m.form.original != nil {
			return []control{{"Save changes", "ctrl+s"}, {"Next field", "enter"}, {"Cancel", "esc"}}
		}
		return []control{{"Next / review", "enter"}, {"Cancel", "esc"}}
	}
	if m.picker {
		return []control{{"Review roles", "enter"}, {"Cancel", "esc"}}
	}
	if m.review != nil {
		switch m.review.kind {
		case "connection":
			label := "Connect terminal"
			if m.review.connection.Kind == "db" && !m.review.connection.IAMDatabase() {
				label = "Open tunnel"
			}
			c := []control{{label, "enter"}}
			if m.review.connection.Kind == "ec2" {
				c = append(c, control{"New window", "w"})
			}
			c = append(c, control{"Edit", "e"})
			if m.review.connection.Kind == "db" && !m.review.connection.IAMDatabase() {
				c = append(c, control{"Set up IAM", "l"})
			}
			if m.review.duplicate {
				c = append(c, control{"New shell", "N"})
			}
			return append(c, control{"Back", "esc"})
		case "run":
			var c []control
			if m.canFocusRun(m.review.run) {
				c = append(c, control{"Focus terminal", "enter"})
			}
			return append(c, control{"Disconnect / stop", "d"}, control{"Recheck", "r"}, control{"Back", "esc"})
		case "role":
			c := []control{{"Renew", "r"}, {"Remove", "d"}}
			if pause, ok := pauseControl(m.review.role); ok {
				c = append(c, pause)
			}
			return append(c, control{"Copy profile", "c"}, control{"Back", "esc"})
		default:
			return []control{{"Confirm", "enter"}, {"Cancel", "esc"}}
		}
	}
	switch m.screen {
	case ec2, databases:
		var c []control
		if m.selectedRow >= 0 && m.selectedRow < len(m.targets()) {
			if m.screen == ec2 {
				c = append(c, control{"Connect terminal", "enter"}, control{"New window", "w"}, control{"Edit", "e"}, control{"Details", "v"})
			} else {
				label := "Connect terminal"
				if !m.targets()[m.selectedRow].IAMDatabase() {
					label = "Open tunnel"
				}
				c = append(c, control{label, "enter"})
				c = append(c, control{"Edit", "e"}, control{"Details", "v"})
				if !m.targets()[m.selectedRow].IAMDatabase() {
					c = append(c, control{"Set up IAM", "l"})
				}
			}
		}
		return c
	case sessions, tunnels:
		rs := m.visibleRuns()
		if m.selectedRow >= 0 && m.selectedRow < len(rs) {
			label := "View session"
			if m.screen == tunnels {
				label = "View route"
			}
			if m.canFocusRun(rs[m.selectedRow]) {
				label = "Focus terminal"
			}
			c := []control{{label, "enter"}}
			return append(c, control{"Disconnect / stop", "d"}, control{"Details", "v"}, control{"Recheck", "r"})
		}
	case credentials:
		var c []control
		if len(m.roles) > 0 {
			c = append(c, control{"Renew", "r"}, control{"Remove", "d"}, control{"Details", "v"})
			if m.selectedRow >= 0 && m.selectedRow < len(m.roles) {
				if pause, ok := pauseControl(m.roles[m.selectedRow]); ok {
					c = append(c, pause)
				}
				c = append(c, control{"Copy profile", "c"})
			}
		}
		return append(c, control{"Retry cleanup", "R"})
	case backup:
		return []control{{"Export bookmarks", "e"}, {"Import file", "i"}}
	}
	return nil
}

// Keep common actions at the left edge and inspection/configuration at the right.
// The same controls drive labels and hit targets; no separate mouse-only actions.
func (m *Model) controlGroups(compact bool) (primary, secondary []control) {
	for _, c := range m.controls() {
		secondaryAction := false
		if !m.busy && !m.help && m.form == nil && !m.picker {
			if m.review != nil {
				secondaryAction = c.key == "esc" || c.key == "e" || c.key == "l" || c.key == "N"
				if m.review.kind == "run" {
					secondaryAction = secondaryAction || c.key == "r"
				}
			} else {
				switch m.screen {
				case ec2, databases:
					secondaryAction = c.key == "e" || c.key == "v" || c.key == "l"
				case sessions, tunnels:
					secondaryAction = c.key == "v" || c.key == "r"
				case credentials:
					secondaryAction = c.key == "R" || c.key == "v" || c.key == "c"
				}
			}
		}
		if secondaryAction {
			secondary = append(secondary, c)
		} else {
			primary = append(primary, c)
		}
	}
	if compact && !m.busy && m.form == nil && m.review == nil && !m.picker && !m.help {
		secondary = append(secondary, control{"Accounts", "accounts"})
	}
	return primary, secondary
}
