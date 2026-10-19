package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

const warningColor = "#FFBF00"

type palette struct{ background, surface, text, muted, border, blue, selection, green, orange, red string }

func (m *Model) palette() palette {
	// RGB-to-index conversion can turn blue-gray text into cyan/teal.
	// Use explicit neutral text and semantic accents on limited-color terminals.
	switch lipgloss.ColorProfile() {
	case termenv.ANSI:
		if m.dark {
			return palette{"0", "0", "15", "7", "8", "12", "0", "10", warningColor, "9"}
		}
		return palette{"15", "15", "0", "8", "12", "4", "15", "2", warningColor, "1"}
	case termenv.ANSI256:
		if m.dark {
			return palette{"232", "233", "255", "248", "239", "111", "235", "114", warningColor, "210"}
		}
		// The indexed palette has no near-white blue; use white instead of a harsh tint.
		return palette{"231", "231", "235", "242", "153", "25", "231", "29", warningColor, "161"}
	}
	if !m.dark {
		return palette{"#FFFFFF", "#F5FAFF", "#213046", "#65748A", "#D7E7F7", "#2059B5", "#EDF5FF", "#216B4D", warningColor, "#AC3547"}
	}
	return palette{"#10151E", "#121923", "#DCE5F1", "#94A3B8", "#2A3545", "#90B9FF", "#192333", "#86D0AD", warningColor, "#EE9EAA"}
}
func (m *Model) paint(s, fg, bg string, bold bool) string {
	if m.ascii {
		s = strings.NewReplacer("–", "-", " · ", " | ").Replace(s)
	}
	if m.noColor {
		return s
	}
	style := lipgloss.NewStyle().Bold(bold)
	if fg != "" {
		style = style.Foreground(lipgloss.Color(fg))
	}
	if bg != "" {
		style = style.Background(lipgloss.Color(bg))
	}
	return style.Render(s)
}

// Success colors belong to the status itself, never the surrounding row.
func (m *Model) paintSuccess(s, fg, bg string, bold bool) string {
	for _, match := range []struct{ prefix, status, suffix string }{
		{" | ", "Connected", " |"},
		{" | ", "Listening", " |"},
		{" | ", "active", ""},
		{"Role credentials: ", "active", ""},
	} {
		at := strings.Index(s, match.prefix+match.status+match.suffix)
		if at < 0 {
			continue
		}
		at += len(match.prefix)
		end := at + len(match.status)
		tail := m.paint(s[end:], fg, bg, bold)
		if match.prefix == "Role credentials: " {
			dot := "●"
			if m.ascii {
				dot = "+"
			}
			if i := strings.LastIndex(s[end:], dot); i >= 0 {
				i += end
				tail = m.paint(s[end:i], fg, bg, bold) + m.paint(dot, m.palette().green, bg, bold) + m.paint(s[i+len(dot):], fg, bg, bold)
			}
		}
		return m.paint(s[:at], fg, bg, bold) + m.paint(s[at:end], m.palette().green, bg, bold) + tail
	}
	return m.paint(s, fg, bg, bold)
}

func padLine(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-lipgloss.Width(s)))
}
func (m *Model) rule(width int) string {
	ch := "─"
	if m.ascii {
		ch = "-"
	}
	p := m.palette()
	return m.paint(strings.Repeat(ch, max(0, width)), p.border, p.background, false)
}
func (m *Model) navigation(width, x, y int) []string {
	p := m.palette()
	labels := make([]string, len(screenNames))
	for i, name := range screenNames {
		labels[i] = fmt.Sprintf("%d %s", i+1, name)
		if screen(i) == m.screen {
			labels[i] = "[" + labels[i] + "]"
		}
	}
	spacing := 2
	groupWidth := func(start, end int) int {
		n := 0
		for i := start; i < end; i++ {
			n += lipgloss.Width(labels[i])
		}
		return n + spacing*(end-start-1)
	}
	left, right := groupWidth(0, 2), groupWidth(2, len(labels))
	if left+2+right > width {
		spacing = 1
		left, right = groupWidth(0, 2), groupWidth(2, len(labels))
	}
	var lines []string
	line, underline := "", ""
	gap := func(n int) {
		line += m.paint(strings.Repeat(" ", max(0, n)), p.text, p.background, false)
		underline += m.rule(max(0, n))
	}
	flush := func() {
		gap(width - lipgloss.Width(line))
		lines = append(lines, line, underline)
		line, underline = "", ""
	}
	for i, label := range labels {
		if i == 2 {
			if left+2+right > width {
				flush()
				gap(width - right)
			} else {
				gap(width - left - right)
			}
		} else if i > 0 {
			gap(spacing)
		}
		offset, size := lipgloss.Width(line), lipgloss.Width(label)
		selected := screen(i) == m.screen
		color, bg := p.muted, p.background
		border := p.border
		if selected {
			color, bg, border = p.blue, p.selection, p.blue
		}
		line += m.paint(label, color, bg, selected)
		ch := "─"
		if m.ascii {
			ch = "-"
		}
		underline += m.paint(strings.Repeat(ch, size), border, p.background, false)
		for row := 0; row < 2; row++ {
			m.hit(x+offset, y+len(lines)+row, size, mouseAction{kind: "screen", index: i})
		}
	}
	flush()
	return lines
}

// Only the body scrolls; identity and navigation stay visible during reviews.
func (m *Model) surface(content []string, width, height int, sidebar bool, x, y int) []string {
	p := m.palette()
	inner := max(1, width-4)
	var wrapped []string
	var actions []mouseAction
	for _, line := range content {
		action := mouseAction{}
		if queue := m.rowActions[line]; len(queue) > 0 {
			action = queue[0]
			m.rowActions[line] = queue[1:]
		}
		parts := wrapDetailLine(line, inner)
		if sidebar {
			parts = []string{ansi.Truncate(safe(line), inner, "...")}
		}
		for _, part := range parts {
			wrapped = append(wrapped, part)
			actions = append(actions, action)
		}
		// Space resource-table entries without expanding forms or the account sidebar.
		// The gap belongs to the same row for mouse selection.
		if !sidebar && action.kind == "row" {
			wrapped = append(wrapped, "")
			actions = append(actions, action)
		}
	}
	content = wrapped
	limit := 0
	if len(content) > height {
		limit = max(0, len(content)-max(1, height-1))
	}
	if sidebar {
		m.maxSidebarScroll = limit
		m.sidebarScroll = min(m.sidebarScroll, limit)
	} else {
		m.maxScroll = limit
		m.scroll = min(m.scroll, limit)
	}
	start, count := m.windowRange(content, height, sidebar)
	content = m.window(content, max(1, height), sidebar)
	out := make([]string, 0, height)
	for i := 0; i < height; i++ {
		line := ""
		if i < len(content) {
			line = ansi.Truncate(content[i], inner, "...")
		}
		if i < count {
			m.hit(x+2, y+i, inner, actions[start+i])
		}
		fg, bg := p.text, p.background
		if sidebar {
			bg = p.surface
		}
		selected := strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "› ")
		if i < count && actions[start+i].kind == "row" {
			selected = actions[start+i].index == m.selectedRow
		}
		if selected {
			fg, bg = p.blue, p.selection
		}
		if strings.HasPrefix(line, "SSO source") {
			fg = p.muted
		}
		if strings.HasPrefix(line, "Accounts") || strings.HasPrefix(line, "  Last") || strings.HasPrefix(line, "  Profile:") || strings.HasPrefix(line, "↑/↓") || strings.HasPrefix(line, "Tab:") || strings.HasPrefix(line, "y:") {
			fg = p.muted
		}
		if strings.HasPrefix(line, "- ") {
			fg = p.blue
		}
		profileStatus := ""
		if i < count && actions[start+i].kind == "profile" {
			profileStatus = m.loginBadge(m.services.Profiles[actions[start+i].index])
			switch profileStatus {
			case "Active", "Unpublished":
				fg = p.text
				if m.dark {
					fg = "#FFFFFF"
				}
			case "Logged out":
				fg = p.muted
			}
		}
		if strings.Contains(line, "[unavailable]") {
			fg = p.red
		}
		if strings.Contains(line, " | Unknown |") || strings.HasSuffix(line, " | expired") {
			fg = p.orange
		}
		if line == "---" {
			out = append(out, m.paint("  ", fg, bg, false)+m.rule(inner)+m.paint("  ", fg, bg, false))
			continue
		}
		if !sidebar && (m.screen == sessions || m.screen == tunnels) && i < count && actions[start+i].kind == "row" {
			runs := m.visibleRuns()
			index := actions[start+i].index
			if index >= 0 && index < len(runs) {
				switch runs[index].State {
				case "Ended", "Stopped", "Failed":
					fg = p.muted
				case "", "Unknown", "Needs login", "Check failed", "Port in use", "Checking":
					fg = p.orange
				default:
					fg = p.green
				}
			}
		}
		padded := "  " + padLine(line, inner) + "  "
		headingFocused := sidebar && strings.HasPrefix(line, "Accounts") && m.focus == 0
		if headingFocused {
			fg = p.blue
		}
		rendered := m.paintSuccess(padded, fg, bg, headingFocused)
		if dot, green := profileDot(profileStatus, m.ascii); green {
			if at := strings.Index(padded, dot); at >= 0 {
				rendered = m.paint(padded[:at], fg, bg, false) + m.paint(dot, p.green, bg, false) + m.paint(padded[at+len(dot):], fg, bg, false)
			}
		}
		for _, badge := range []string{"[AWS]", "[EC2]", "[DB]"} {
			if at := strings.Index(padded, badge); at >= 0 {
				color := p.orange
				if badge == "[DB]" {
					color = "#7450B0"
					if m.dark {
						color = "#C0A6ED"
					}
				}
				rendered = m.paint(padded[:at], fg, bg, false) + m.paint(badge, color, bg, false) + m.paintSuccess(padded[at+len(badge):], fg, bg, false)
				break
			}
		}
		if i < count && actions[start+i].kind == "help-heading" {
			rendered = m.paint(padded, p.blue, bg, true)
		}
		if i < count && actions[start+i].kind == "help-key" {
			separator := " │ "
			if m.ascii {
				separator = " | "
			}
			if key, action, found := strings.Cut(padded, separator); found {
				rendered = m.paint(key, p.blue, bg, true) + m.paint(separator, p.border, bg, false) + m.paint(action, p.text, bg, false)
			}
		}
		if i < count && actions[start+i].kind == "field" && m.form != nil {
			rendered = m.paint("  ", fg, bg, false) + m.formComponent(actions[start+i].index, inner) + m.paint("  ", fg, bg, false)
		}
		out = append(out, rendered)
	}
	return out
}
func (m *Model) workspaceView() string {
	p := m.palette()
	lines := m.appTabs(0)
	suffix := " | ?: help | Ctrl+T: theme"
	keys := strings.ReplaceAll(m.footer(), " | ?: help", "")
	footer := []string{ansi.Truncate(keys, max(1, m.width-2-lipgloss.Width(suffix)), "…") + suffix}
	notice := m.status
	if notice == "" {
		notice = m.contextualNotice()
	}
	height := max(1, m.height-len(lines)-len(footer)-1)
	sideWidth := 0
	if m.width >= 100 {
		sideWidth = 28
		if m.width >= 120 {
			sideWidth = 32
		}
	}
	mainWidth := m.width
	if sideWidth > 0 {
		mainWidth -= sideWidth + 1
	}
	inner := max(1, mainWidth-4)
	warning := strings.HasPrefix(notice, "Warning:")
	errorNotice := strings.HasPrefix(notice, "Error:")
	if warning {
		icon := "⚠"
		if m.ascii {
			icon = "!"
		}
		notice = icon + "  " + notice
	}
	notice = safe(m.activity() + notice)
	if warning {
		if profile, ok := m.profile(); ok {
			label := "[" + safe(profile.Name) + "]"
			if before, after, found := strings.Cut(notice, "profile "+label); found {
				// Inline highlighting resets ANSI state; explicitly resume warning colors.
				notice = before + "profile " + m.paint(label, p.blue, p.selection, true) + m.paint(after, p.orange, p.background, false)
			}
		}
	}
	statusLines := strings.Split(ansi.Wrap(notice, inner, " "), "\n")
	if len(statusLines) > 2 {
		statusLines = statusLines[:2]
		statusLines[1] = ansi.Truncate(statusLines[1], max(1, inner-3), "") + "..."
	}
	for i := range statusLines {
		statusLines[i] = "  " + statusLines[i]
	}
	mainX := 0
	if sideWidth > 0 {
		mainX = sideWidth + 1
	}
	var main []string

	if m.height >= 30 {
		main = append(main, m.paint(strings.Repeat(" ", mainWidth), p.text, p.background, false))
	}
	identity := m.identity()
	profileActive := m.selectedProfileActive()
	actionParts := m.buttonParts(control{m.accountActionLabel(), "ctrl+a"}, m.height >= 34)
	actionWidth := lipgloss.Width(actionParts[0])
	var identityLines []string
	var accountLines []bool
	for _, line := range identity {
		width := inner
		if len(identityLines) < len(actionParts) {
			width = max(1, inner-actionWidth-2)
		}
		for _, wrapped := range wrapLines([]string{line}, width) {
			identityLines = append(identityLines, wrapped)
			accountLines = append(accountLines, strings.HasPrefix(line, "Account:"))
		}
	}
	for len(identityLines) < len(actionParts) {
		identityLines = append(identityLines, "")
		accountLines = append(accountLines, false)
	}
	for i, line := range identityLines {
		fg := p.muted
		if profileActive && accountLines[i] {
			fg = p.orange
		}
		if profileActive && strings.HasPrefix(line, "Profile:") {
			fg = p.blue
		}
		if profileActive && strings.HasPrefix(line, "Role:") {
			fg = p.text
		}
		if strings.HasPrefix(line, "Role credentials: active") {
			fg = p.green
		}
		if strings.HasPrefix(line, "Role credentials:") {
			icon := "○"
			if strings.HasPrefix(line, "Role credentials: active") {
				icon = "●"
			} else if strings.HasPrefix(line, "Role credentials: Logged out") {
				fg = p.muted
			} else {
				fg = p.orange
			}
			if m.ascii {
				icon = "!"
				if strings.HasPrefix(line, "Role credentials: active") {
					icon = "+"
				}
			}
			line += "  " + icon
		}
		if i < len(actionParts) {
			buttonColor := p.blue
			if profileActive {
				buttonColor = p.red
			}
			enabled := !m.busy && m.form == nil && m.review == nil && !m.picker && !m.help
			if !enabled {
				buttonColor = p.muted
			} else {
				m.hit(mainX+2+inner-actionWidth, len(lines)+len(main), actionWidth, mouseAction{kind: "key", key: "ctrl+a"})
			}
			main = append(main, m.paint("  "+padLine(line, inner-actionWidth), fg, p.background, true)+m.paint(actionParts[i], buttonColor, m.buttonBackground(), true)+m.paint("  ", fg, p.background, false))
		} else {
			main = append(main, m.paint("  "+padLine(line, inner)+"  ", fg, p.background, false))
		}
	}
	if m.height >= 30 {
		main = append(main, m.paint(strings.Repeat(" ", mainWidth), p.text, p.background, false))
	}
	main = append(main, m.rule(mainWidth))
	for _, line := range m.navigation(inner, mainX+2, len(lines)+len(main)) {
		main = append(main, m.paint("  ", p.text, p.background, false)+line+m.paint("  ", p.text, p.background, false))
	}
	title := screenNames[m.screen]
	switch m.screen {
	case ec2:
		title = "[EC2] Saved instances"
	case databases:
		title = "[DB] Saved databases"
	case sessions:
		title = "Sessions / all accounts"
	case tunnels:
		title = "Tunnels / all accounts"
	}
	content := strings.Split(m.content(), "\n")
	if m.help {
		title = "Keyboard help"
	} else if m.form != nil {
		title = "Edit " + m.form.kind
	} else if m.review != nil {
		title = "Review / " + screenNames[m.screen]
		if m.review.kind == "connection" {
			title = "Review connection"
			if m.review.details {
				title = "Connection details"
			}
		} else if m.review.kind == "run" || m.review.kind == "role" {
			title = "Details / " + screenNames[m.screen]
		}
	} else if m.picker {
		title = "Log in"
	}
	compactPicker := sideWidth == 0 && m.focus == 0 && m.form == nil && m.review == nil && !m.picker && !m.help
	if compactPicker {
		title = "Accounts / Tab: EC2"
		content = strings.Split(m.sidebar(), "\n")
	}
	focus := "  "
	if m.focus == 1 || sideWidth == 0 {
		focus = "> "
	}
	if strings.HasPrefix(title, ">_ ") {
		focus = ""
	}
	connectionList := (m.screen == ec2 || m.screen == databases) && m.form == nil && m.review == nil && !m.help && !m.picker && !compactPicker
	if connectionList {
		buttonParts := m.buttonParts(control{"+ New connection", "n"}, m.height >= 34)
		buttonWidth := lipgloss.Width(buttonParts[0])
		labelWidth := max(1, inner-buttonWidth-2)
		color := m.secondaryButtonForeground()
		if m.busy {
			color = p.muted
		}
		for i, button := range buttonParts {
			label := ""
			if i == len(buttonParts)/2 {
				label = focus + title
			}
			if !m.busy {
				m.hit(mainX+mainWidth-2-buttonWidth, len(lines)+len(main), buttonWidth, mouseAction{kind: "key", key: "n"})
			}
			main = append(main, m.paint("  "+padLine(ansi.Truncate(label, labelWidth, "…"), labelWidth)+"  ", p.text, p.background, true)+m.paint(button, color, m.buttonBackground(), false)+m.paint("  ", p.text, p.background, false))
		}
		targets := m.targets()
		if m.selectedRow >= 0 && m.selectedRow < len(targets) {
			c := targets[m.selectedRow]
			profile, _ := m.profile()
			details := m.connectionDetails(c, profile, false)
			// Separate the section heading from the title without a redundant rule.
			details = append([]string{"", "Selected connection"}, details[2:]...)
			if c.Kind == "db" {
				login := "Tunnel only · Edit to enable PostgreSQL IAM"
				if isDocumentDB(c) && !c.IAMDatabase() {
					login = documentDBLogin
				}
				if c.IAMDatabase() {
					login = c.DBUser + " @ " + c.Database + " · PostgreSQL IAM"
				}
				details = []string{"", "Selected connection", m.detailRow("Name", c.Name), m.detailRow("Endpoint", c.Endpoint), m.detailRow("Login", login), m.detailRow("Route", fmt.Sprintf("localhost:%d → DB:%d · %s", c.LocalPort, c.DBPort, c.Region))}
				if hint := m.databaseTunnelHint(c); hint != "" {
					details = append(details, m.detailRow("Open tunnel", hint))
				}
			}
			if m.height < 34 {
				details = []string{"Selected connection: " + c.Name, "Instance: " + c.InstanceID + " · " + c.Region}
				if c.Kind == "db" {
					details = []string{"Selected connection: " + c.Name, "Database: " + c.Endpoint + " · " + c.Region, "Login: tunnel only (Edit to enable PostgreSQL IAM)"}
					if c.IAMDatabase() {
						details[2] = "PostgreSQL IAM: " + c.DBUser + " @ " + c.Database
					} else if isDocumentDB(c) {
						details[2] = "Login: tunnel only (your DocumentDB user stays in your client)"
					}
					if hint := m.databaseTunnelHint(c); hint != "" {
						details[2] = "Open tunnel: " + hint
					}
				}
				if m.height >= 30 {
					details = append([]string{""}, details...)
				}
			}
			for _, detail := range details {
				if detail == "---" {
					main = append(main, m.rule(mainWidth))
					continue
				}
				namePrefix := ""
				if detail == m.detailRow("Name", c.Name) {
					namePrefix = m.detailRow("Name", "")
				}
				if m.height < 34 && detail == "Selected connection: "+c.Name {
					namePrefix = "Selected connection: "
				}
				if namePrefix != "" {
					name := ansi.Truncate(safe(c.Name), max(1, inner-lipgloss.Width(namePrefix)), "…")
					tail := strings.Repeat(" ", max(0, inner-lipgloss.Width(namePrefix)-lipgloss.Width(name))) + "  "
					main = append(main, m.paint("  "+namePrefix, p.text, p.background, false)+m.paint(name, p.blue, p.selection, true)+m.paint(tail, p.text, p.background, false))
					continue
				}
				main = append(main, m.paint("  "+padLine(ansi.Truncate(safe(detail), inner, "…"), inner)+"  ", p.text, p.background, detail == "Selected connection"))
			}
		}
	} else {
		main = append(main, m.paint(padLine(ansi.Truncate("  "+focus+title, mainWidth, "..."), mainWidth), p.text, p.background, true))
	}
	if (m.screen == sessions || m.screen == tunnels) && m.form == nil && m.review == nil && !m.help && !m.picker && !compactPicker {
		rs := m.visibleRuns()
		if m.selectedRow >= 0 && m.selectedRow < len(rs) {
			r := rs[m.selectedRow]
			summary := "Selected: " + r.Connection.Name + " · " + runState(r)
			identity := "Run profile: " + r.Profile.Name + " · " + r.Profile.AccountID
			if r.Connection.Kind == "db" {
				identity += fmt.Sprintf(" · localhost:%d", r.Connection.LocalPort)
			}
			for _, line := range []string{summary, identity} {
				main = append(main, m.paint("  "+padLine(ansi.Truncate(safe(line), inner, "…"), inner)+"  ", p.text, p.background, false))
			}
		}
	}
	if len(m.controls()) > 0 && m.height >= 30 {
		main = append(main, m.paint(strings.Repeat(" ", mainWidth), p.text, p.background, false))
	}
	for _, line := range m.toolbar(inner, mainX+2, len(lines)+len(main), sideWidth == 0) {
		main = append(main, m.paint("  ", p.text, p.background, false)+line+m.paint("  ", p.text, p.background, false))
	}
	if m.height >= 34 || (connectionList && m.height >= 30) {
		main = append(main, m.paint(strings.Repeat(" ", mainWidth), p.text, p.background, false))
	}
	bodyHeight := max(1, height-len(main)-len(statusLines)-1)
	main = append(main, m.surface(content, mainWidth, bodyHeight, compactPicker, mainX, len(lines)+len(main))...)

	main = append(main, m.rule(mainWidth))
	for _, line := range statusLines {
		fg := p.muted
		if warning || m.busy {
			fg = p.orange
		}
		if errorNotice {
			fg = p.red
		}
		clipped := ansi.Truncate(line, mainWidth, "...")
		padding := strings.Repeat(" ", max(0, mainWidth-lipgloss.Width(clipped)))
		main = append(main, m.paint(clipped, fg, p.background, false)+m.paint(padding, fg, p.background, false))
	}
	if len(main) > height {
		height = len(main)
	}
	if sideWidth > 0 {
		label := "Accounts / workspaces"
		borderColor := p.border
		if m.focus == 0 {
			borderColor = p.blue
		}
		side := m.surface(append([]string{label, ""}, strings.Split(m.sidebar(), "\n")...), sideWidth, max(1, height-4), true, 0, len(lines))
		for _, hint := range []string{"", "↑/↓: profile", "Tab: EC2", "y: edit account label"} {
			side = append(side, m.paint(padLine("  "+hint, sideWidth), p.muted, p.surface, false))
		}
		divider := "│"
		if m.ascii {
			divider = "|"
		}
		for i := 0; i < height; i++ {
			lines = append(lines, side[i]+m.paint(divider, borderColor, p.surface, false)+main[i])
		}
	} else {
		lines = append(lines, main...)
	}
	lines = append(lines, m.rule(m.width))
	for _, line := range footer {
		m.hit(m.width-1-len("Ctrl+T: theme"), len(lines), len("Ctrl+T: theme"), mouseAction{kind: "theme"})
		m.hit(m.width-1-len("Ctrl+T: theme")-3-len("?: help"), len(lines), len("?: help"), mouseAction{kind: "key", key: "?"})
		lines = append(lines, m.paint(strings.Repeat(" ", max(0, m.width-1-lipgloss.Width(strings.TrimSpace(line))))+strings.TrimSpace(line)+" ", p.muted, p.surface, false))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) toolbar(width, x, y int, compact bool) []string {
	p := m.palette()
	primary, secondary := m.controlGroups(compact)
	if len(primary)+len(secondary) == 0 {
		return nil
	}
	tall := m.height >= 34
	buttonHeight := 1
	if tall {
		buttonHeight = 3
	}
	var lines []string
	row := make([]string, buttonHeight)
	used := 0
	flush := func() {
		for _, line := range row {
			lines = append(lines, line+m.paint(strings.Repeat(" ", max(0, width-lipgloss.Width(line))), p.text, p.background, false))
		}
		row = make([]string, buttonHeight)
		used = 0
	}
	gap := func(n int) {
		for j := range row {
			row[j] += m.paint(strings.Repeat(" ", n), p.text, p.background, false)
		}
		used += n
	}
	render := func(c control, bold, secondary bool) {
		buttonWidth := lipgloss.Width(m.buttonLabel(c)) + 2
		a := mouseAction{kind: "key", key: c.key}
		if c.key == "accounts" {
			a.kind = "accounts"
		}
		fg, bg := p.blue, m.buttonBackground()
		// Neutral secondary actions remain visibly enabled in either theme,
		// including when the group wraps. Cancel follows the same rule in forms.
		if secondary || c.key == "esc" {
			fg, bold = m.secondaryButtonForeground(), false
		}
		if m.busy && m.operationButton == c.key {
			fg = p.orange
			a = mouseAction{}
		}
		if c.key == "d" {
			fg = p.red
		}
		for j, part := range m.buttonParts(c, tall) {
			row[j] += m.paint(part, fg, bg, bold)
			m.hit(x+used, y+len(lines)+j, buttonWidth, a)
		}
		used += buttonWidth
	}
	for i, c := range primary {
		buttonWidth := lipgloss.Width(m.buttonLabel(c)) + 2
		if used > 0 && used+2+buttonWidth > width {
			flush()
		}
		if used > 0 {
			gap(2)
		}
		render(c, i == 0, false)
	}
	// Pack each secondary row, then align the entire group to the right. At
	// narrow widths it moves below the primary group without splitting buttons.
	for len(secondary) > 0 {
		total, count := 0, 0
		for _, c := range secondary {
			next := lipgloss.Width(m.buttonLabel(c)) + 2
			if count > 0 {
				next += 2
			}
			if total+next > width && count > 0 {
				break
			}
			total += next
			count++
		}
		if used > 0 && used+2+total > width {
			flush()
		}
		gap(max(0, width-used-total))
		for i, c := range secondary[:count] {
			if i > 0 {
				gap(2)
			}
			render(c, false, true)
		}
		secondary = secondary[count:]
		if len(secondary) > 0 {
			flush()
		}
	}
	flush()
	return lines
}

func shortcutLabel(key string) string {
	switch key {
	case "ctrl+a":
		return "Ctrl+A"
	case "enter":
		return "Enter"
	case "esc":
		return "Esc"
	case "accounts":
		return "Tab"
	case " ":
		return "Space"
	default:
		return key
	}
}

func (m *Model) buttonLabel(c control) string {
	if c.key == "accounts" {
		return "  " + c.label + "  "
	}
	hint := shortcutLabel(c.key)
	if m.busy && m.operationButton == c.key {
		hint = m.spinnerFrame()
	}
	return "  " + c.label + " (" + hint + ")  "
}

// White outlines in dark mode; dark outlines on white in light mode.
// Muted text is reserved for disabled buttons rather than secondary actions.
func (m *Model) secondaryButtonForeground() string {
	if m.dark {
		return "#FFFFFF"
	}
	return m.palette().text
}

// Light-mode buttons are outlines on the surrounding workspace, with no fill.
func (m *Model) buttonBackground() string {
	p := m.palette()
	if !m.dark {
		return p.background
	}
	return p.surface
}

// Header and toolbar actions share border geometry, padding and shortcut labels.
func (m *Model) buttonParts(c control, tall bool) []string {
	label := m.buttonLabel(c)
	left, right, tl, tr, bl, br, h := "│", "│", "╭", "╮", "╰", "╯", "─"
	if m.ascii {
		left, right, tl, tr, bl, br, h = "|", "|", "+", "+", "+", "+", "-"
	}
	if !tall {
		return []string{left + label + right}
	}
	rule := strings.Repeat(h, lipgloss.Width(label))
	return []string{tl + rule + tr, left + label + right, bl + rule + br}
}
