package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type animationMsg time.Time

func animate() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return animationMsg(t) })
}

type themeSavedMsg struct{ err error }

func (m *Model) toggleTheme() tea.Cmd {
	m.dark = !m.dark
	theme := "light"
	if m.dark {
		theme = "dark"
	}
	if m.themeSaving {
		m.themePending = theme
		return nil
	}
	return m.persistTheme(theme)
}

func (m *Model) persistTheme(theme string) tea.Cmd {
	if m.services.SaveTheme == nil {
		return nil
	}
	m.themeSaving = true
	save := m.services.SaveTheme
	return func() tea.Msg { return themeSavedMsg{save(theme)} }
}

func (m *Model) syncInput() {
	if m.form == nil || m.form.index >= len(m.form.fields) {
		return
	}
	f := m.form.fields[m.form.index]
	if m.inputForm != m.form || m.inputIndex != m.form.index || m.input.Value() != f.value {
		m.input = textinput.New()
		m.input.Prompt = ""
		m.input.Placeholder = "Type here"
		m.input.CharLimit = 4096
		m.input.SetValue(f.value)
		m.input.CursorEnd()
		m.input.Focus()
		m.input.Cursor.SetMode(cursor.CursorStatic)
		m.inputForm, m.inputIndex = m.form, m.form.index
	}
}

func (m *Model) editInput(k tea.KeyMsg) tea.Cmd {
	m.syncInput()
	if k.Type == tea.KeyRunes {
		k.Runes = []rune(safe(string(k.Runes)))
	}
	if k.String() == "ctrl+u" {
		m.input.SetValue("")
	} else {
		m.input, _ = m.input.Update(k)
	}
	m.form.fields[m.form.index].value = m.input.Value()
	return nil
}

// Components are rendered after text sanitization; user data never supplies ANSI.
func (m *Model) formComponent(index, width int) string {
	f := m.form.fields[index]
	p := m.palette()
	selected := index == m.form.index
	prefix, value := "  ", f.value
	bg, fg := p.surface, p.text
	if selected {
		m.syncInput()
		m.input.Width = max(1, width-len(f.label)-5)
		m.input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.blue)).Background(lipgloss.Color(p.selection))
		m.input.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(p.muted)).Background(lipgloss.Color(p.selection))
		m.input.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(p.background)).Background(lipgloss.Color(p.blue))
		value = m.input.View()
		if m.noColor {
			value = ansi.Strip(value)
		}
		prefix, bg, fg = "> ", p.selection, p.blue
	}
	return m.paint(padLine(ansi.Truncate(prefix+f.label+": "+value, width, "…"), width), fg, bg, selected)
}

func (m *Model) contextualNotice() string {
	if m.form != nil {
		if m.form.original != nil {
			return "Edit saved connection. Changes apply to future connections; open sessions keep their original route."
		}
		return "Enter connection details; review the identity before connecting."
	}
	if m.review != nil {
		return "Verify account, exact role and target before confirming."
	}
	switch m.screen {
	case sessions:
		return "Enter focuses the selected terminal when available; v shows identity and status."
	case tunnels:
		return "An open forwarding tunnel is not database authentication."
	case credentials:
		return "Credential status and expiry reflect the locally managed AWS result."
	case backup:
		return "Export bookmarks or preview an import before applying changes."
	}
	return "Select a saved connection or press n to add one."
}

func (m *Model) activity() string {
	if !m.busy {
		return ""
	}
	return m.spinnerFrame() + " elapsed " + clockDuration(time.Since(m.operationStarted)) + "  "
}

func (m *Model) spinnerFrame() string {
	if m.activitySpinner.Spinner.FPS == 0 {
		m.activitySpinner = spinner.New(spinner.WithSpinner(spinner.Line))
	}
	return ansi.Strip(m.activitySpinner.View())
}

// A countdown is only shown for a known, active credential expiry.
func credentialCountdown(expiration time.Time, now time.Time) string {
	if expiration.IsZero() {
		return "Expiry unknown"
	}
	if !expiration.After(now) {
		return "Expired"
	}
	return "Expires in " + clockDuration(expiration.Sub(now)+time.Second-1)
}
func clockDuration(d time.Duration) string {
	seconds := int64(max(time.Duration(0), d) / time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, (seconds%3600)/60, seconds%60)
}
