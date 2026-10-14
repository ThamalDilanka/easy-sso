package ui

import (
	"context"
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Terminal selection is independent of the AWS workspace selection and profile.
func (m *Model) embeddedRun() (domain.Run, bool) {
	if m.services.Terminals != nil && m.services.Terminals.Has(m.terminalID) {
		for _, r := range m.terminalTabs {
			if r.ID == m.terminalID {
				return r, true
			}
		}
	}
	return domain.Run{}, false
}

func (m *Model) showTerminal(r domain.Run) {
	for i, tab := range m.terminalTabs {
		if tab.ID == r.ID {
			m.terminalTabs[i] = r
			m.terminalID = r.ID
			m.terminalFocus = true
			return
		}
	}
	m.terminalTabs = append(m.terminalTabs, r)
	m.terminalID, m.terminalFocus = r.ID, true
}

func (m *Model) selectTab(index int) {
	m.terminalID, m.terminalFocus = "", false
	if index > 0 && index <= len(m.terminalTabs) {
		m.terminalID, m.terminalFocus = m.terminalTabs[index-1].ID, true
	}
}

func (m *Model) cycleTab(step int) {
	index := 0
	for i, r := range m.terminalTabs {
		if r.ID == m.terminalID {
			index = i + 1
		}
	}
	m.selectTab((index + step + len(m.terminalTabs) + 1) % (len(m.terminalTabs) + 1))
}

// Closing never detaches a running shell. Keep its tab until the owner confirms
// local cleanup, including when the user changes tabs during the operation.
func (m *Model) closeTerminalTab() tea.Cmd {
	if m.busy {
		return nil
	}
	run, ok := m.embeddedRun()
	if !ok {
		return nil
	}
	m.selectTab(0)
	return m.operation("Close terminal", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
		owner, ok := m.services.Sessions.(interface {
			CloseShell(context.Context, string) error
		})
		if !ok {
			return run, domain.ImportPlan{}, errors.New("terminal owner cannot confirm cleanup; tab remains open")
		}
		return run, domain.ImportPlan{}, owner.CloseShell(ctx, run.ID)
	})
}

func (m *Model) removeTerminalTab(id string) {
	for i, r := range m.terminalTabs {
		if r.ID == id {
			m.terminalTabs = append(m.terminalTabs[:i], m.terminalTabs[i+1:]...)
			break
		}
	}
	if m.terminalID == id {
		m.selectTab(0)
	}
}

func (m *Model) terminalView() string {
	lines := m.appTabs(0)
	run, ok := m.embeddedRun()
	if !ok {
		m.selectTab(0)
		return m.workspaceView()
	}
	if (run.State == "Failed" || run.Connection.IAMDatabase()) && run.Error != "" {
		p := m.palette()
		notice := wrapLines([]string{"Connection failed: " + run.Error}, max(1, m.width-4))
		for _, line := range notice[:min(len(notice), max(1, m.height/3))] {
			lines = append(lines, m.paint(padLine("  "+line, m.width), p.red, p.background, true))
		}
	}
	lines = append(lines, m.terminalPane(run, m.width, max(1, m.height-len(lines)-1), 0, len(lines))...)
	p := m.palette()
	hint := "Shift+Tab: next tab | Ctrl+]: AWS | Ctrl+W: close session | Ctrl+C: interrupt"
	lines = append(lines, m.paint(padLine(ansi.Truncate(hint, m.width, "…"), m.width), p.muted, p.surface, false))
	return strings.Join(lines, "\n")
}

func (m *Model) terminalPane(run domain.Run, width, height, x, y int) []string {
	p := m.palette()
	inner := max(1, width-4)
	rows := make([]string, 0, height)
	bodyHeight := max(1, height)
	frame, err := m.services.Terminals.Frame(run.ID, inner, bodyHeight, m.terminalFocus)
	if err != nil {
		frame = err.Error()
	}
	output := strings.Split(frame, "\n")
	for i := 0; i < bodyHeight; i++ {
		line := ""
		if i < len(output) {
			line = output[i]
		}
		if m.noColor {
			line = ansi.Strip(line)
		}
		// Only emulator-rendered cells are admitted here; the normal text sanitizer
		// would destroy the screen's styling. Constrain every row to this pane.
		line = ansi.Truncate(line, inner, "")
		line += strings.Repeat(" ", max(0, inner-lipgloss.Width(line)))
		rows = append(rows, m.paint("  ", p.text, p.background, false)+line+m.paint("  ", p.text, p.background, false))
		m.hit(x+2, y+i, inner, mouseAction{kind: "terminal"})
	}
	return rows
}
