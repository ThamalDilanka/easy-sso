package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/charmbracelet/x/ansi"
)

func sameProfile(a, b domain.Profile) bool {
	return a.Name == b.Name && a.AccountID == b.AccountID && a.Role == b.Role && a.Partition == b.Partition
}
func roleActive(r domain.ManagedRole) bool {
	return strings.EqualFold(r.Status, "active") && r.Expiration.After(time.Now())
}

// roleUnpublished reports a role whose entry is out of the credentials file
// while its keys are held and still valid; an expired or edited one is reported
// by its real status so the user is sent to Renew, not Republish.
func roleUnpublished(r domain.ManagedRole) bool {
	return managedStatus(r) == "unpublished"
}

// selectedProfileActive also counts unpublished roles, so Log out stays available.
func (m *Model) selectedProfileActive() bool {
	p, ok := m.profile()
	if !ok {
		return false
	}
	for _, r := range m.roles {
		if sameProfile(r.Profile, p) && (roleActive(r) || roleUnpublished(r)) {
			return true
		}
	}
	return false
}
func (m *Model) accountActionLabel() string {
	if m.selectedProfileActive() {
		return "Log out"
	}
	return "Log in"
}
func (m *Model) contentWidth() int {
	width := m.width - 4
	if m.width >= 120 {
		width -= 33
	} else if m.width >= 100 {
		width -= 29
	}
	return max(1, width)
}

// Left-aligned field labels form a consistent details table. Values remain
// complete, including long endpoints, account identities and cross-region routes.
func (m *Model) detailSection(title string, fields [][2]string) []string {
	lines := []string{title, "---"}
	for _, f := range fields {
		if f[1] == "" {
			f[1] = "—"
		}
		lines = append(lines, m.detailRow(f[0], f[1]))
	}
	return lines
}
func (m *Model) listTable(headers []string, rows [][]string) []string {
	width := m.contentWidth() - 2
	indexWidth := max(2, len(fmt.Sprint(len(rows))))
	widths := []int{max(6, width-32-indexWidth-2), 16, 12}
	if len(headers) == 4 {
		widths = []int{max(6, width-46-indexWidth-2), 14, 10, 16}
	}
	if len(headers) >= 3 && headers[2] == "STARTED (LOCAL)" {
		widths = []int{max(6, width-40-indexWidth-2), 14, 20}
		if len(headers) == 4 {
			widths = []int{max(6, width-58-indexWidth), 14, 20, 16}
		}
	}
	row := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, cell := range cells {
			parts[i] = padLine(ansi.Truncate(safe(cell), widths[i], "…"), widths[i])
		}
		return strings.Join(parts, "  ")
	}
	lines := []string{"  " + padLine("#", indexWidth) + "  " + row(headers), "  " + strings.Repeat("-", width)}
	for i, cells := range rows {
		lines = append(lines, m.clickable(m.row(padLine(fmt.Sprint(i+1), indexWidth)+"  "+row(cells), i == m.selectedRow), mouseAction{kind: "row", index: i}))
	}
	return lines
}
func (m *Model) connectionList(items []domain.Connection) string {
	rows := make([][]string, 0, len(items))
	for _, c := range items {
		status := "Saved"
		for _, r := range m.runs {
			if r.Connection.ID == c.ID && activeRun(r) {
				status = runState(r)
				if c.Kind == "db" {
					if p, ok := m.profile(); ok && !sameProfile(r.Profile, p) {
						status = "Other role"
					}
				}
				break
			}
		}
		opened := "Never"
		if c.LastUsedAt != nil {
			opened = relativeTime(*c.LastUsedAt, time.Now())
		}
		rows = append(rows, []string{c.Name, c.Region, status, opened})
	}
	lines := m.listTable([]string{"NAME", "REGION", "STATUS", "LAST OPENED"}, rows)
	_, embedded := m.embeddedRun()
	if m.screen != ec2 && m.screen != databases && !embedded && m.selectedRow >= 0 && m.selectedRow < len(items) {
		c := items[m.selectedRow]
		p, _ := m.profile()
		lines = append(lines, "")
		lines = append(lines, m.connectionDetails(c, p, false)...)
	}
	return strings.Join(lines, "\n")
}

// A workspace selection does not change the identity of an existing tunnel.
func (m *Model) databaseTunnelHint(c domain.Connection) string {
	for i := len(m.runs) - 1; i >= 0; i-- {
		r := m.runs[i]
		if r.Connection.Kind == "db" && r.Connection.AccountID == c.AccountID && r.Connection.Partition == c.Partition && r.Connection.Endpoint == c.Endpoint && r.Connection.DBPort == c.DBPort && r.Connection.Region == c.Region && (r.State == "Listening" || r.State == "Connected") {
			return fmt.Sprintf("profile [%s] · %s", r.Profile.Name, localRoute(r.Connection))
		}
	}
	return ""
}

// documentDBDatabase names the database mongosh opens; blank means its default.
func documentDBDatabase(c domain.Connection) string {
	if c.Database == "" {
		return "(default: test)"
	}
	return c.Database
}

// runRoleARN is the $external user name: the role ARN of the run's own
// verified identity, never the role selected in the workspace now.
func runRoleARN(r domain.Run) string {
	arn, err := profiles.IAMRoleARN(r.Identity)
	if err != nil {
		return "Unavailable for this identity"
	}
	return arn
}

func databaseLoginGuidance() []string {
	return []string{
		"Connect terminal generates the IAM token automatically; no token to copy.",
		"Admin setup: enable RDS IAM; grant the DB user rds_iam and SQL access.",
		"SSO role: allow rds-db:connect for the DB resource ID and database username.",
		"Saving login settings does not configure AWS or database permissions.",
		"Waiting for setup? Set Auth to tunnel and use your existing database login in a client.",
	}
}

func documentLabel(c domain.Connection) string {
	if c.DocumentName == "" {
		return "Default (Session Manager preferences)"
	}
	return c.DocumentName
}

func (m *Model) connectionDetails(c domain.Connection, p domain.Profile, reviewing bool) []string {
	fields := [][2]string{{"Name", c.Name}}
	if c.Kind == "ec2" {
		fields = append(fields, [2]string{"Instance", c.InstanceID}, [2]string{"Region", c.Region}, [2]string{"Document", documentLabel(c)})
	} else {
		fields = append(fields, [2]string{"DB endpoint", fmt.Sprintf("%s:%d", c.Endpoint, c.DBPort)}, [2]string{"Engine", engineLabel(c)}, [2]string{"DB region", c.Region}, [2]string{"Relay", c.RelayInstanceID}, [2]string{"SSM region", c.RelayRegion}, [2]string{"Local route", localRoute(c)})
		if c.IAMDatabase() {
			fields = append(fields, [2]string{"Login", "PostgreSQL IAM"}, [2]string{"Database", c.Database}, [2]string{"DB user", c.DBUser})
		} else if c.DocDBIAM() {
			fields = append(fields, [2]string{"Login", documentDBIAMLogin}, [2]string{"Database", documentDBDatabase(c)})
		} else if isDocumentDB(c) {
			fields = append(fields, [2]string{"Login", documentDBLogin})
		} else {
			fields = append(fields, [2]string{"Login", "Tunnel only · Edit to enable PostgreSQL IAM"})
		}
		if reviewing {
			fields = append(fields, [2]string{"DB account", c.DBAccountID + " (unverified ownership)"})
		}
	}
	lines := m.detailSection("CONNECTION", fields)
	if reviewing {
		lines = append(lines, "")
		lines = append(lines, m.detailSection("IDENTITY", [][2]string{{"Account", c.AccountID}, {"Profile", p.Name}, {"Role", p.Role}, {"SSO source", p.SSOSession}, {"Partition", c.Partition}})...)
		if len(p.Chain) > 1 {
			lines = append(lines, "Source chain: "+strings.Join(p.Chain, " -> "))
		}
		if c.Kind == "db" {
			lines = append(lines, "p: edit local port")
			if c.IAMDatabase() {
				lines = append(lines, databaseLoginGuidance()...)
			} else if c.DocDBIAM() {
				lines = append(lines, "Connect terminal opens mongosh logged in with this role; no password.", "Admin setup: create your role ARN as a user in $external with mechanism MONGODB-AWS.")
			} else if isDocumentDB(c) {
				lines = append(lines, "Tunnel transport only; DataGrip or any MongoDB client logs in with your DocumentDB user.", "")
				lines = append(lines, m.documentDBClient(c, m.documentDBPort(c))...)
			} else {
				lines = append(lines, "Tunnel transport only; use an existing database login in your database client.", "Set up IAM login (l) saves the database name and username; an administrator must configure IAM access first.")
			}
		}
	} else {
		used := "Never"
		if c.LastUsedAt != nil {
			used = relativeTime(*c.LastUsedAt, time.Now())
		}
		lines = append(lines, m.detailRow("Last opened", used))
	}
	return lines
}
func (m *Model) runList(items []domain.Run) string {
	rows := make([][]string, 0, len(items))
	for _, r := range items {
		started := "Unavailable"
		if !r.StartedAt.IsZero() {
			started = r.StartedAt.Local().Format("02 Jan 2006 15:04")
		}
		row := []string{r.Connection.Name, runState(r), started}
		if m.screen == sessions {
			row = append(row, m.runTerminal(r))
		}
		rows = append(rows, row)
	}
	headers := []string{"NAME", "STATUS", "STARTED (LOCAL)"}
	if m.screen == sessions {
		headers = append(headers, "TERMINAL")
	}
	lines := m.listTable(headers, rows)
	if m.screen == sessions {
		lines = append([]string{"Shown: " + m.sessionSummary(items), "Newest first · latest 50 records. Unverified means status is unconfirmed.", ""}, lines...)
	}
	if m.screen == tunnels {
		lines = append([]string{"Shown: " + m.sessionSummary(items), "Newest first · old tunnels are checked automatically. Use r to retry.", ""}, lines...)
	}
	return strings.Join(lines, "\n")
}
func (m *Model) runDetails(r domain.Run) []string {
	title := "SESSION"
	if r.Connection.Kind == "db" {
		title = "TUNNEL"
	}
	lines := m.detailSection(title, [][2]string{{"Target", r.Connection.Name}, {"State", runState(r)}, {"Account", r.Profile.AccountID}, {"Profile", r.Profile.Name}, {"Role", r.Profile.Role}, {"Region", r.Connection.Region}, {"Started", humanTimestamp(r.StartedAt)}, {"SSM session", r.SessionID}})
	if r.Connection.Kind == "db" {
		lines = append(lines, m.detailSection("ROUTE", [][2]string{{"Local", localRoute(r.Connection)}, {"Remote", fmt.Sprintf("%s:%d", r.Connection.Endpoint, r.Connection.DBPort)}, {"Relay", r.Connection.RelayInstanceID}, {"SSM region", r.Connection.RelayRegion}})...)
		if r.Connection.IAMDatabase() {
			lines = append(lines, m.detailRow("Database", r.Connection.Database), m.detailRow("DB user", r.Connection.DBUser), m.detailRow("Local tab", m.runTerminal(r)))
		} else if r.Connection.DocDBIAM() {
			lines = append(lines, m.detailRow("Login", documentDBIAMLogin), m.detailRow("Database", documentDBDatabase(r.Connection)), m.detailRow("DB user", runRoleARN(r)), m.detailRow("Local tab", m.runTerminal(r)))
		}
	} else {
		lines = append(lines, m.detailRow("Instance", r.Connection.InstanceID), m.detailRow("Terminal", r.Title), m.detailRow("Local tab", m.runTerminal(r)))
	}
	lines = append(lines, m.detailRow("Session", r.ID))
	if r.Error != "" {
		label := "Error: "
		if r.State == "Remote active" || r.State == "Checking" || r.State == "Stopped" {
			label = "Status: "
		}
		lines = append(lines, label+r.Error)
	}
	if isDocumentDB(r.Connection) && activeTunnel(r) {
		lines = append(lines, "")
		lines = append(lines, m.documentDBClient(r.Connection, r.Connection.LocalPort)...)
	}
	return lines
}

func (m *Model) detailRow(label, value string) string {
	separator := " │ "
	if m.ascii {
		separator = " | "
	}
	return padLine(safe(label), 16) + separator + safe(value)
}

// Continuation lines stay under the value column instead of jumping back under labels.
func wrapDetailLine(line string, width int) []string {
	for _, separator := range []string{" │ ", " | "} {
		at := strings.Index(line, separator)
		if at == 16 && width > 19 {
			prefix := line[:at+len(separator)]
			values := wrapLines([]string{line[at+len(separator):]}, width-19)
			out := []string{prefix + values[0]}
			for _, value := range values[1:] {
				out = append(out, strings.Repeat(" ", 16)+separator+value)
			}
			return out
		}
	}
	return wrapLines([]string{line}, width)
}

func relativeTime(value, now time.Time) string {
	if value.IsZero() {
		return "Never"
	}
	elapsed := now.Sub(value)
	future := elapsed < 0
	if future {
		elapsed = -elapsed
	}
	if elapsed < time.Minute {
		if future {
			return "In less than a minute"
		}
		return "Just now"
	}
	count, unit := int(elapsed/time.Minute), "minute"
	switch {
	case elapsed >= 24*time.Hour:
		count, unit = int(elapsed/(24*time.Hour)), "day"
	case elapsed >= time.Hour:
		count, unit = int(elapsed/time.Hour), "hour"
	}
	if count != 1 {
		unit += "s"
	}
	if future {
		return fmt.Sprintf("In %d %s", count, unit)
	}
	return fmt.Sprintf("%d %s ago", count, unit)
}

// The record lifecycle and local PTY ownership are independent.
func (m *Model) runTerminal(r domain.Run) string {
	if m.services.Terminals != nil && m.services.Terminals.Has(r.ID) {
		for _, tab := range m.terminalTabs {
			if tab.ID == r.ID {
				return "Open tab"
			}
		}
		return "Hidden tab"
	}
	if r.State == "Ended" || r.State == "Failed" || r.State == "Stopped" {
		return "History"
	}
	return "No local tab"
}

func (m *Model) sessionSummary(items []domain.Run) string {
	active, unknown, history, tabs, attention := 0, 0, 0, 0, 0
	for _, r := range items {
		switch r.State {
		case "Ended", "Failed", "Stopped":
			history++
		case "", "Unknown":
			unknown++
		case "Needs login", "Check failed", "Port in use", "Checking":
			attention++
		default:
			active++
		}
		if m.runTerminal(r) == "Open tab" {
			tabs++
		}
	}
	if attention > 0 || m.screen == tunnels {
		return fmt.Sprintf("%d active · %d need attention · %d history | %d open tabs", active, attention+unknown, history, tabs)
	}
	return fmt.Sprintf("%d active · %d unverified · %d history | %d open tabs", active, unknown, history, tabs)
}
