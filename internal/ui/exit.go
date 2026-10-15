package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Exit warnings describe resources we can identify, never inferred live shells
// from historical or unverified registrations.
func (m *Model) exitWarnings() []string {
	shells, tunnels, external, clients := 0, 0, 0, 0
	for _, run := range m.runs {
		if !activeRun(run) {
			continue
		}
		if run.Connection.Kind == "ec2" {
			if m.services.Terminals != nil && m.services.Terminals.Has(run.ID) {
				shells++
			} else if run.State == "Connected" || run.State == "Connecting" {
				external++
			}
		} else if run.Connection.Kind == "db" {
			if m.services.Terminals != nil && m.services.Terminals.Has(run.ID) {
				clients++
			}
			if owner, ok := m.services.Sessions.(interface{ OwnsTunnel(string) bool }); ok && owner.OwnsTunnel(run.ID) {
				tunnels++
			}
		}
	}
	var lines []string
	if shells > 0 {
		lines = append(lines, exitCount(shells, "local shell", "local shells")+" will be disconnected.")
	}
	if clients > 0 {
		lines = append(lines, exitCount(clients, "database terminal", "database terminals")+" will be disconnected.")
	}
	if tunnels > 0 {
		lines = append(lines, exitCount(tunnels, "owned DB tunnel", "owned DB tunnels")+" will be stopped.")
	}
	if external > 0 {
		lines = append(lines, exitCount(external, "independent external shell", "independent external shells")+" will remain open.")
	}
	unpublished := 0
	for _, r := range m.roles {
		if roleUnpublished(r) {
			unpublished++
		}
	}
	if unpublished > 0 {
		lines = append(lines, exitCount(unpublished, "unpublished credential entry", "unpublished credential entries")+" will lose held keys on exit; renew after restarting.")
	}
	if len(m.roles) > 0 {
		lines = append(lines, exitCount(len(m.roles), "saved login credential entry", "saved login credential entries")+" are saved on this device.", "Exit keeps credentials. Clear creds & exit removes easy-sso managed credentials.")
	}
	return lines
}

func (m *Model) requestExit() tea.Cmd {
	m.help, m.picker = false, false
	m.scroll = 0
	m.status = ""
	m.review = &review{kind: "exit"}
	if len(m.exitWarnings()) == 0 {
		return m.exitWorkspace()
	}
	return nil
}

// Even an exit without a confirmation runs the normal owned-resource cleanup.
func (m *Model) exitWorkspace() tea.Cmd {
	return m.exitWithCredentials(false)
}

func (m *Model) exitWithCredentials(clear bool) tea.Cmd {
	return m.operation("Exit", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
		if m.services.Sessions != nil {
			if err := m.services.Sessions.Shutdown(ctx); err != nil {
				return domain.Run{}, domain.ImportPlan{}, err
			}
		}
		if clear && m.services.Credentials != nil {
			roles, err := m.services.Credentials.List()
			if err != nil {
				return domain.Run{}, domain.ImportPlan{}, err
			}
			var errs []error
			for _, role := range roles {
				if err := m.services.Credentials.Remove(role.Alias); err != nil {
					errs = append(errs, err)
				}
			}
			if err := errors.Join(errs...); err != nil {
				return domain.Run{}, domain.ImportPlan{}, fmt.Errorf("could not clear all managed credentials: %w", err)
			}
		}
		return domain.Run{}, domain.ImportPlan{}, nil
	})
}

func (m *Model) exitView() string {
	p := m.palette()
	cardWidth := max(8, min(92, m.width-4))
	inner := max(1, cardWidth-6)
	var lines []string
	add := func(text, color string, bold bool) {
		for _, line := range wrapLines([]string{text}, inner) {
			lines = append(lines, m.paint(padLine(line, inner), color, p.surface, bold))
		}
	}
	add("Exit workspace?", p.text, true)
	add("", p.text, false)
	for _, warning := range m.exitWarnings() {
		add(warning, p.orange, false)
		add("", p.text, false)
	}
	if len(m.roles) > 0 {
		add("Clearing does not sign out external shells or revoke copied credentials.", p.muted, false)
		add("", p.text, false)
	}
	var buttons []hitRegion
	if m.busy {
		add("Closing workspace…", p.orange, false)
	} else {
		if strings.HasPrefix(m.status, "Error:") {
			add(m.status, p.red, false)
			add("", p.text, false)
		}
		row := []string{"", "", ""}
		used := 0
		flush := func() {
			for _, line := range row {
				lines = append(lines, line+m.paint(strings.Repeat(" ", max(0, inner-lipgloss.Width(line))), p.text, p.surface, false))
			}
			row, used = []string{"", "", ""}, 0
		}
		for i, c := range []control{{"Exit", "enter"}, {"Clear creds & exit", "c"}, {"Cancel", "esc"}} {
			if inner < 30 && i == 1 {
				c.label = "Clear"
			}
			parts := m.buttonParts(c, true)
			w := min(inner, lipgloss.Width(parts[0]))
			if used > 0 && used+2+w > inner {
				flush()
			}
			if used > 0 {
				for j := range row {
					row[j] += m.paint("  ", p.text, p.surface, false)
				}
				used += 2
			}
			color := p.blue
			if i == 1 {
				color = p.orange
			}
			if i == 2 {
				color = m.secondaryButtonForeground()
			}
			for j, part := range parts {
				row[j] += m.paint(ansi.Truncate(part, w, ""), color, p.surface, i != 2)
				buttons = append(buttons, hitRegion{x: used, y: len(lines) + j, width: w, action: mouseAction{kind: "key", key: c.key}})
			}
			used += w
		}
		flush()
	}
	tl, tr, bl, br, h, v := "╭", "╮", "╰", "╯", "─", "│"
	if m.ascii {
		tl, tr, bl, br, h, v = "+", "+", "+", "+", "-", "|"
	}
	border := func(text string) string { return m.paint(text, p.border, p.surface, false) }
	card := []string{border(tl + strings.Repeat(h, cardWidth-2) + tr), border(v) + m.paint(strings.Repeat(" ", cardWidth-2), p.text, p.surface, false) + border(v)}
	for _, line := range lines {
		card = append(card, border(v)+m.paint("  ", p.text, p.surface, false)+line+m.paint("  ", p.text, p.surface, false)+border(v))
	}
	card = append(card, border(v)+m.paint(strings.Repeat(" ", cardWidth-2), p.text, p.surface, false)+border(v), border(bl+strings.Repeat(h, cardWidth-2)+br))
	left, top := max(0, (m.width-cardWidth)/2), max(0, (m.height-len(card))/2)
	for _, button := range buttons {
		m.hit(left+3+button.x, top+2+button.y, button.width, button.action)
	}
	output := make([]string, max(m.height, len(card)))
	for y := range output {
		if y >= top && y < top+len(card) {
			output[y] = m.paint(strings.Repeat(" ", left), p.text, p.background, false) + card[y-top] + m.paint(strings.Repeat(" ", max(0, m.width-left-cardWidth)), p.text, p.background, false)
		} else {
			output[y] = m.paint(strings.Repeat(" ", max(1, m.width)), p.text, p.background, false)
		}
	}
	return strings.Join(output, "\n")
}

func exitCount(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}
