package ui

import (
	"github.com/charmbracelet/x/ansi"
	"strings"
)

// A sliding strip always keeps the AWS tab and the active terminal visible.
func (m *Model) appTabs(y int) []string {
	p := m.palette()
	line, bottom := "", ""
	add := func(label string, a mouseAction, selected bool) {
		x := ansi.StringWidth(line)
		for row := 0; row < 2; row++ {
			m.hit(x, y+row, ansi.StringWidth(label), a)
		}
		fg, bg := p.muted, p.surface
		if selected {
			fg, bg = p.blue, p.selection
		}
		if before, after, ok := strings.Cut(label, "[AWS]"); ok {
			line += m.paint(before, fg, bg, selected) + m.paint("[AWS]", p.orange, bg, true) + m.paint(after, fg, bg, selected)
		} else {
			line += m.paint(label, fg, bg, selected)
		}
		border, color := "─", p.border
		if m.ascii {
			border = "-"
		}
		if selected {
			color = p.blue
		}
		bottom += m.paint(strings.Repeat(border, ansi.StringWidth(label)), color, p.surface, false)
	}
	add(" [AWS] easy-sso ", mouseAction{kind: "tab", index: 0}, m.terminalID == "")
	if len(m.terminalTabs) > 0 {
		add(" < ", mouseAction{kind: "tab-prev"}, false)
		capacity := max(1, (m.width-ansi.StringWidth(line)-3)/29)
		active := 0
		for i, r := range m.terminalTabs {
			if r.ID == m.terminalID {
				active = i
			}
		}
		start := max(0, active-capacity+1)
		for i := start; i < len(m.terminalTabs) && i < start+capacity; i++ {
			r := m.terminalTabs[i]
			label := r.Title
			if label == "" {
				label = r.Connection.Name + " · " + r.Profile.Name
			}
			add(" ["+padLine(ansi.Truncate(safe(label), 22, "…"), 22)+"] ", mouseAction{kind: "tab", index: i + 1}, r.ID == m.terminalID)
			closeLabel := " × "
			if m.ascii {
				closeLabel = " x "
			}
			add(closeLabel, mouseAction{kind: "tab-close", index: i + 1}, r.ID == m.terminalID)
		}
		add(" > ", mouseAction{kind: "tab-next"}, false)

	}
	fill := m.paint(strings.Repeat(" ", max(0, m.width-ansi.StringWidth(line))), p.text, p.surface, false)
	return []string{line + fill, bottom + m.rule(max(0, m.width-ansi.StringWidth(line)))}
}
