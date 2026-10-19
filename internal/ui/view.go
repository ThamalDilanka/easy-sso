package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/lipgloss"
)

func (m *Model) accountAlias(id string) string {
	if a := m.aliases[id]; a != "" {
		return safe(a)
	}
	return id
}
func (m *Model) accountLabel(id string) string {
	alias := m.accountAlias(id)
	if alias == id {
		return id
	}
	return alias + " (" + id + ")"
}
func (m *Model) marker(selected bool) string {
	if selected {
		if m.ascii {
			return ">"
		}
		return "›"
	}
	return " "
}
func (m *Model) row(s string, selected bool) string { return m.marker(selected) + " " + safe(s) }
func managedStatus(r domain.ManagedRole) string {
	if strings.EqualFold(r.Status, "missing") {
		return "missing"
	}
	if strings.EqualFold(r.Status, "conflict") {
		return "conflict"
	}
	if strings.EqualFold(r.Status, "unknown") || r.Status == "" {
		return "unknown"
	}
	if !r.Expiration.After(time.Now()) || strings.EqualFold(r.Status, "expired") {
		return "expired"
	}
	if strings.EqualFold(r.Status, "unpublished") {
		return "unpublished"
	}
	if strings.EqualFold(r.Status, "active") {
		return "active until " + humanTimestamp(r.Expiration)
	}
	return "unknown"
}
func (m *Model) roleStatus(p domain.Profile) string {
	status := "missing"
	for _, r := range m.roles {
		if sameProfile(r.Profile, p) {
			current := managedStatus(r)
			if strings.HasPrefix(current, "active until ") {
				return current
			}
			status = current
		}
	}
	return status
}

// These labels describe application-managed role keys, not the browser SSO session.
func (m *Model) loginBadge(p domain.Profile) string {
	status := m.roleStatus(p)
	switch {
	case strings.HasPrefix(status, "active until "):
		return "Active"
	case status == "missing" || status == "expired":
		return "Logged out"
	case status == "conflict":
		return "Conflict"
	case status == "unpublished":
		return "Unpublished"
	default:
		return "Unknown"
	}
}
func (m *Model) loginStatus(p domain.Profile) string {
	status := m.roleStatus(p)
	if strings.HasPrefix(status, "active until ") {
		return status
	}
	if status == "expired" {
		return "Logged out (keys expired)"
	}
	if status == "missing" {
		return "Logged out (no active keys)"
	}
	if status == "unpublished" {
		return "Unpublished (keys held in easy-sso; republish in Credentials)"
	}
	return m.loginBadge(p)
}

func (m *Model) identity() []string {
	p, ok := m.profile()
	if !ok {
		return []string{"Identity: no selected profile"}
	}
	account := p.AccountID
	if alias := m.accountAlias(p.AccountID); alias != p.AccountID {
		account += " | " + alias
	}
	lines := []string{"Account: " + account, "Profile: " + p.Name, "Role: " + p.Role, "SSO source: " + p.SSOSession + " | Partition: " + p.Partition, "Role credentials: " + m.identityCredentialStatus(p)}
	if len(p.Chain) > 1 {
		lines = append(lines, "Source chain: "+strings.Join(p.Chain, " -> "))
	}
	if p.Error != "" {
		lines = append(lines, "Unavailable: "+p.Error)
	}
	return lines
}

// profileDot is the sidebar marker for a profile's credential state: filled
// while keys are published, hollow otherwise, and green whenever this app holds
// usable keys for it, published or not.
func profileDot(badge string, ascii bool) (dot string, green bool) {
	filled, hollow := "●", "○"
	if ascii {
		filled, hollow = "+", "-"
	}
	switch badge {
	case "Active":
		return filled, true
	case "Unpublished":
		return hollow, true
	}
	return hollow, false
}
func (m *Model) sidebar() string {
	var lines []string
	lastSource, lastAccount := "", ""
	// Profiles are sorted by root login source, destination account, and name.
	for i, p := range m.services.Profiles {
		source := p.SSOSession
		if source == "" {
			source = "Unavailable profiles"
		}
		if source != lastSource {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "[AWS] "+source, "SSO source: "+source)
			lastSource = source
			lastAccount = ""
		}
		if p.AccountID != lastAccount {
			branch := "+ "
			if m.accountOpen(p) {
				branch = "- "
			}
			if m.accountAlias(p.AccountID) != p.AccountID {
				lines = append(lines, m.clickable(branch+m.accountAlias(p.AccountID), mouseAction{kind: "account", index: i}))
				branch = "  "
			}
			lines = append(lines, m.clickable(branch+p.AccountID, mouseAction{kind: "account", index: i}))
			lastAccount = p.AccountID
		}
		// Keep the source/account tree visible; expand the selected account.
		// Arrow navigation follows every profile and expands its account.
		if !m.accountOpen(p) {
			continue
		}
		badge := m.loginBadge(p)
		dot, _ := profileDot(badge, m.ascii)
		label := "  " + dot + " " + p.Name
		// Colour carries the unpublished state; only ASCII or no-colour output spells it out.
		if badge != "Active" && badge != "Logged out" && (badge != "Unpublished" || m.ascii || m.noColor) {
			label += " · " + badge
		}
		if p.Error != "" {
			label += " [unavailable]"
		}
		lines = append(lines, m.clickable(m.row(label, i == m.selectedProfile), mouseAction{kind: "profile", index: i}))
	}
	if len(lines) == 0 {
		lines = []string{"No profiles configured"}
	}
	// Sidebar helpers are pinned by workspaceView.
	return strings.Join(lines, "\n")
}
func (m *Model) View() string {
	m.hits = nil
	m.rowActions = map[string][]mouseAction{}
	if m.review != nil && m.review.kind == "exit" {
		return m.exitView()
	}
	if m.width < 80 || m.height < 20 {
		lines := append([]string{"[AWS] easy-sso"}, m.identity()...)
		if m.review != nil && m.review.kind == "exit" {
			lines = append(lines, m.reviewContent()...)
		} else {
			lines = append(lines, "Resize terminal to at least 80 columns and 20 rows.", "q / Ctrl+C: exit review")
		}
		return strings.Join(wrapLines(lines, max(1, m.width)), "\n")
	}
	if m.terminalID != "" {
		return m.terminalView()
	}
	return m.workspaceView()
}
func (m *Model) footer() string {
	if run, ok := m.embeddedRun(); ok && m.terminalFocus && m.form == nil && m.review == nil && !m.help {
		if run.Connection.Kind == "ec2" && m.terminalEnded(run) {
			if m.reconnecting(run) {
				return "Reconnecting | Esc: cancel | Ctrl+]: AWS workspace"
			}
			return "Session ended | r: reconnect | Ctrl+W: close | Ctrl+]: AWS workspace"
		}
		return "Terminal focused | Ctrl+]: AWS workspace | All other keys go to the server"
	}

	if m.help {
		return "Esc / ?: close help | PgUp/PgDn: scroll"
	}
	if m.busy && m.authURL != "" {
		return "o: open browser | c: copy URL | Esc: cancel"
	}
	if m.busy {
		return "Esc: cancel | q: exit after cleanup"
	}
	if m.form != nil {
		if m.form.original != nil {
			return "Tab/↑/↓: fields | Ctrl+S: save | Enter: next/save | Ctrl+U: clear | Esc: cancel"
		}
		return "Tab/↑/↓: fields | Shift+Tab: next tab | Enter: next/review | Ctrl+U: clear | Esc: back"
	}
	if m.review != nil {
		switch m.review.kind {
		case "run":
			keys := "d: disconnect/stop | r: recheck | PgUp/PgDn: scroll | Esc: back"
			if m.canFocusRun(m.review.run) {
				keys = "Enter/f: focus terminal | " + keys
			}
			return keys
		case "role":
			return "r: renew | d: remove | u: unpublish/republish | c: copy profile | PgUp/PgDn: scroll | Esc: back"
		case "connection":
			return "Enter: connect | e: edit | PgUp/PgDn: scroll | Esc: back"
		}
		return "Enter: confirm | Esc: cancel | ?: help"
	}
	if m.picker {
		return "↑/↓: roles | Space: select | Enter: review | Esc: cancel"
	}
	if m.focus == 0 {
		return "↑/↓: profile | Enter: workspace | Ctrl+A: " + m.accountActionLabel() + " | y: label | Tab: Accounts/EC2/DB | Shift+Tab: next tab | ?: help"
	}
	common := " | Tab: Accounts/EC2/DB | Shift+Tab: next tab | 1-6: views | ?: help | q: exit"
	switch m.screen {
	case ec2, databases:
		if m.screen == ec2 {
			return "↑/↓: select | Enter: connect | v: details | n: new | e: edit" + common
		}
		if ts := m.targets(); m.selectedRow >= 0 && m.selectedRow < len(ts) && isDocumentDB(ts[m.selectedRow]) {
			return "↑/↓: select | Enter: open tunnel | v: details | c: copy URL | n: new | e: edit" + common
		}
		return "↑/↓: select | Enter: connect | v: details | l: IAM setup | n: new | e: edit" + common
	case sessions, tunnels:
		action := "Enter: details"
		rs := m.visibleRuns()
		if m.selectedRow >= 0 && m.selectedRow < len(rs) && m.canFocusRun(rs[m.selectedRow]) {
			action = "Enter/f: focus terminal"
		}
		copyURL := ""
		if m.selectedRow >= 0 && m.selectedRow < len(rs) && isDocumentDB(rs[m.selectedRow].Connection) && activeTunnel(rs[m.selectedRow]) {
			copyURL = " | c: copy URL"
		}
		return action + " | v: details | d: disconnect/stop | r: recheck" + copyURL + common
	case credentials:
		return "Ctrl+A: " + m.accountActionLabel() + " | v: details | r: renew | d: remove | c: cleanup" + common
	case backup:
		return "e: export | i: import and preview" + common
	}
	return common
}
func (m *Model) content() string {
	if m.busy && m.authNotice != "" {
		lines := []string{"Complete sign-in in your browser", m.authFeedback, "", m.authNotice}
		if m.authURL != "" {
			lines = append(lines, "", m.clickable("Open sign-in link: "+m.authURL, mouseAction{kind: "key", key: "o"}), "c: Copy sign-in URL")
		}
		lines = append(lines, "", "Only temporary role keys are kept. SSO tokens are cleared automatically.")
		return strings.Join(lines, "\n")
	}

	if m.help {
		return m.helpContent()
	}
	if m.picker {
		lines := []string{"Log in: select profiles across accounts"}
		for i, p := range m.services.Profiles {
			check := "[ ]"
			if m.picked[i] {
				check = "[x]"
			}
			label := fmt.Sprintf("%s %s | %s | %s | SSO %s", check, p.Name, p.AccountID, p.Role, p.SSOSession)
			if p.Error != "" {
				label += " [unavailable: " + p.Error + "]"
			}
			lines = append(lines, m.clickable(m.row(label, i == m.pickerRow), mouseAction{kind: "pick", index: i}))
		}
		return strings.Join(lines, "\n")
	}
	if m.form != nil {
		var lines []string
		if m.review != nil && m.form.kind == "activation" {
			lines = append(lines, m.reviewContent()...)
		}
		lines = append(lines, "Edit "+m.form.kind)
		for i, f := range m.form.fields {
			lines = append(lines, m.clickable(m.row(f.label+": "+f.value, i == m.form.index), mouseAction{kind: "field", index: i}))
		}
		if m.form.kind == "db" {
			lines = append(lines, "")
			if connections.DocumentDBEndpoint(strings.TrimSpace(m.form.fields[1].value)) {
				lines = append(lines, "DocumentDB: easy-sso opens the tunnel with SSO only; your DocumentDB user stays in your client.", "Keep Auth on tunnel and leave the IAM database fields blank.")
			} else {
				lines = append(lines, databaseLoginGuidance()...)
			}
		}
		if m.form.kind == "activation" {
			check := "[ ]"
			if m.review.acknowledged {
				check = "[x]"
			}
			lines = append(lines, m.clickable(m.row(check+" Acknowledge cleanup (Space), then Enter to log in", m.form.index == len(m.form.fields)), mouseAction{kind: "ack"}))
		}
		return strings.Join(lines, "\n")
	}
	if m.review != nil {
		return strings.Join(m.reviewContent(), "\n")
	}
	var lines []string
	switch m.screen {
	case ec2, databases:
		ts := m.targets()
		if len(ts) == 0 {
			if m.screen == ec2 {
				return "No saved instances here yet.\n\nYour first successful connection will appear here.\nAdd an instance ID and its own region to get started."
			}
			return "No saved databases here yet.\n\nAdd an RDS, Aurora or DocumentDB endpoint and its SSM relay.\nPostgreSQL IAM bookmarks open a terminal; DocumentDB tunnels serve your own client."
		}
		return m.connectionList(ts)
	case sessions, tunnels:
		rs := m.visibleRuns()
		if len(rs) == 0 {
			if m.screen == tunnels {
				return "No application-owned tunnels.\nOpen a saved database connection from view 2.\nDocumentDB tunnels serve DataGrip or any MongoDB client."
			}
			return "No application-owned sessions.\nSelect an instance in EC2 and press Enter to connect.\nSwitch servers in the list; each embedded shell stays alive."
		}
		return m.runList(rs)
	case credentials:
		return m.credentialsContent()
	case backup:
		path := m.services.BookmarkPath
		if path == "" {
			path = filepath.Join(m.services.Prerequisites.StateDir, "connections.json")
		}
		lines = append(lines, "Bookmark file: "+path, fmt.Sprintf("Saved connections: %d", len(m.connections)), "e: Export to a path | i: Import a file and preview merge", "Backup contains resource metadata only; no credentials or session transcripts.")
	}
	return strings.Join(lines, "\n")
}
func runState(r domain.Run) string {
	if r.State == "" || strings.EqualFold(r.State, "unknown") {
		return "Unverified"
	}
	return r.State
}
func (m *Model) reviewContent() []string {
	r := m.review
	var lines []string
	switch r.kind {
	case "connection":
		c := r.connection
		p, _ := m.profile()
		lines = m.connectionDetails(c, p, true)

		if r.duplicate {
			lines = append(lines, "Existing shell: "+r.run.Title, m.terminalHint(r.run), "N: explicitly request another shell")
		} else {
			lines = append(lines, "Enter: verify identity and open | Esc: cancel")
		}
	case "activation":
		lines = append(lines, "Review selected destination roles")
		for _, s := range r.selections {
			lines = append(lines, fmt.Sprintf("%s | %s | %s | SSO %s", s.Profile.Name, s.Profile.AccountID, s.Profile.Role, s.Profile.SSOSession))
			if len(s.Profile.Chain) > 1 {
				lines = append(lines, "Chain: "+strings.Join(s.Profile.Chain, " -> "))
			}
		}
		lines = append(lines, "Existing managed roles are retained.", "Clear cached AWS CLI SSO logins for all profiles in this environment.", "Other CLI SSO workflows may need to sign in again.", "This does not revoke issued role credentials or remove unmanaged copies.")
	case "run", "stop":
		run := r.run
		lines = m.runDetails(run)
		if r.kind == "stop" {
			lines = append(lines, "Enter: stop only this run. Remote termination will be requested where possible.")
		} else {
			lines = append(lines, m.terminalHint(run), "d: Disconnect review | r: Recheck")
		}
	case "role", "remove":
		lines = m.detailSection("CREDENTIAL", [][2]string{{"Managed alias", managedLabel(r.role)}, {"Use", "--profile " + r.role.Alias}, {"Account", r.role.Profile.AccountID}, {"Source profile", r.role.Profile.Name}, {"Role", r.role.Profile.Role}, {"Status", managedStatus(r.role)}, {"Expiry", humanTimestamp(r.role.Expiration)}})

		if r.kind == "remove" {
			lines = append(lines, "Enter: delete only application-owned local profile material.", "Existing sessions and copied credentials are not revoked.")
		} else {
			lines = append(lines, "r: Renew after review | d: Remove after review")
		}
	case "logout":
		lines = []string{"LOG OUT", "", "Remove temporary credentials for this profile?"}
		if len(r.selections) > 0 {
			p := r.selections[0].Profile
			lines = append(lines, m.detailSection("PROFILE", [][2]string{{"Profile", p.Name}, {"Account", p.AccountID}, {"Role", p.Role}})...)
		}
		lines = append(lines, "", "Existing shells and tunnels remain open.", "Enter: log out | Esc: cancel")
	case "import":
		lines = append(lines, "Import preview: merge review", fmt.Sprintf("Additions: %d | Conflicts: %d", len(r.plan.Additions), len(r.plan.Conflicts)))
		for _, c := range r.plan.Additions {
			lines = append(lines, "+ "+importSummary(c))
		}
		for _, c := range r.plan.Conflicts {
			lines = append(lines, "Conflict: "+conflictSummary(c, r.plan.Existing))
		}
		unbound := append([]string(nil), r.plan.Unbound...)
		sort.Strings(unbound)
		for _, name := range unbound {
			lines = append(lines, "Requires profile rebinding: "+name)
		}
		choice := "[ ] Keep local conflicts (default)"
		if r.replace {
			choice = "[x] Replace conflicting local records (explicit opt-in)"
		}
		lines = append(lines, m.clickable(choice, mouseAction{kind: "key", key: " "}), "Space: toggle replacement | Enter: import | Esc: cancel", "Import never connects or changes credentials.")
	case "exit":
		lines = append([]string{"Exit workspace?"}, m.exitWarnings()...)
	}
	return lines
}

// importSummary names a custom session document, so an import cannot quietly
// change which document a shell runs.
func importSummary(c domain.Connection) string {
	line := c.Name + " | " + c.AccountID + " | " + c.Region
	if c.Kind == "ec2" && c.DocumentName != "" {
		line += " | document " + c.DocumentName
	}
	return line
}

// conflictSummary shows when replacing a local bookmark with the same ID would
// change its session document, including removing it.
func conflictSummary(c domain.Connection, existing []domain.Connection) string {
	for _, local := range existing {
		if local.ID == c.ID && local.Kind == "ec2" && local.DocumentName != c.DocumentName {
			return c.Name + " | " + c.AccountID + " | " + c.Region + " | document " + documentLabel(local) + " -> " + documentLabel(c)
		}
	}
	return importSummary(c)
}
func wrapLines(lines []string, width int) []string {
	width = max(1, width)
	var out []string
	for _, line := range lines {
		line = safe(line)
		if line == "" {
			out = append(out, "")
			continue
		}
		var b strings.Builder
		for _, r := range line {
			if lipgloss.Width(b.String()+string(r)) > width {
				out = append(out, b.String())
				b.Reset()
			}
			b.WriteRune(r)
		}
		out = append(out, b.String())
	}
	return out
}

func (m *Model) window(lines []string, height int, sidebar bool) []string {
	if len(lines) <= height {
		return lines
	}
	start, count := m.windowRange(lines, height, sidebar)
	out := append([]string(nil), lines[start:start+count]...)
	return append(out, fmt.Sprintf("... %d-%d of %d | PgUp/PgDn: scroll", start+1, start+count, len(lines)))
}
func (m *Model) windowRange(lines []string, height int, sidebar bool) (int, int) {
	if len(lines) <= height {
		return 0, len(lines)
	}
	count := max(1, height-1)
	start := m.scroll
	focusRow := -1
	if (sidebar && m.sidebarScroll == 0) || (!sidebar && m.scroll == 0 && (m.form != nil || (m.review == nil && !m.help))) {
		for i, line := range lines {
			if strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "› ") {
				focusRow = i
				break
			}
		}
	}
	if sidebar {
		start = m.sidebarScroll
	}
	if focusRow >= 0 {
		if focusRow >= start+count {
			start = focusRow - count + 1
		}
		if focusRow < start {
			start = focusRow
		}
	}
	start = min(max(0, start), len(lines)-count)
	return start, count
}
