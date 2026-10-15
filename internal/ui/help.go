package ui

import "strings"

type shortcut struct{ key, action string }
type shortcutGroup struct {
	title string
	rows  []shortcut
}

// Shortcut descriptions are audited against actions.go; context is part of the
// binding because shell input, forms, and confirmations take precedence.
func shortcutGroups() []shortcutGroup {
	return []shortcutGroup{
		{"WORKSPACE NAVIGATION", []shortcut{
			{"Tab", "Cycle Accounts → EC2 → Databases → Accounts."},
			{"1 / 2", "Open EC2 / Databases."},
			{"3 / 4", "Open Sessions / Tunnels."},
			{"5 / 6", "Open Credentials / Backup."},
			{"Ctrl+Left/Right", "Previous / next view across all six views."},
			{"↑ / ↓ or k / j", "Select a profile in Accounts, or a row in the current view."},
			{"Enter", "Leave Accounts for the current view; otherwise use its action below."},
			{"s", "Open Sessions."},
			{"PgUp / PgDn", "Scroll the workspace body or this help table."},
			{"? / Esc", "Close Help. Outside Help, ? opens it; Esc cancels a review."},
			{"Ctrl+T", "Switch light / dark theme."},
			{"Ctrl+R", "Reload local AWS profile definitions."},
		}},
		{"ACCOUNTS & LOGIN", []shortcut{
			{"Ctrl+A", "Log in to the selected profile, or review logout if active."},
			{"A", "Open advanced multi-profile activation (uppercase A)."},
			{"y", "Edit the selected account's display label (outside the Credentials table, where y copies the profile name)."},
			{"Space", "In the advanced profile picker: select / deselect a role."},
			{"Enter / Esc", "In the picker: review selected roles / cancel."},
		}},
		{"EC2", []shortcut{
			{"Enter", "Connect to the selected instance or focus its local shell tab."},
			{"w", "Start an additional shell in a separate terminal window."},
			{"v", "Show connection details and review."},
			{"n", "Add an instance bookmark."},
			{"e", "Edit the selected instance name, ID and region; also available in review."},
			{"f / N", "In a duplicate-shell review: focus existing / request new shell (uppercase N)."},
		}},
		{"DATABASES", []shortcut{
			{"Enter", "Connect a PostgreSQL IAM terminal, or open a legacy tunnel-only bookmark."},
			{"l", "Set database name/user for IAM login; AWS and DB permissions must already be configured."},
			{"v", "Show database route and login details without connecting."},
			{"n", "Add a database and relay bookmark."},
			{"e", "Edit all saved database and relay fields; also available in review."},
			{"p", "In connection review: temporarily override the local port."},
		}},
		{"SESSIONS & TUNNELS", []shortcut{
			{"Enter / f", "Focus an existing terminal. Enter shows details when no terminal can be focused."},
			{"v", "Open selected run details at the top; Esc returns to the list position."},
			{"d", "Review disconnect / stop for the selected run."},
			{"r", "Recheck status; recovered tunnels use their original profile to verify AWS and local port state."},
		}},
		{"CREDENTIALS", []shortcut{
			{"Enter / v", "Show the selected managed credential's details."},
			{"r", "Renew the selected managed role through login / activation."},
			{"d", "Review removal of the selected managed credentials."},
			{"c", "Retry pending AWS CLI SSO cleanup."},
			{"y", "Copy the published profile name (the value for --profile) to the clipboard."},
			{"p", "Pause or resume: a paused role is taken out of the AWS credentials file and its keys stay only in this app's memory until resumed; exiting drops them."},
		}},
		{"BACKUP", []shortcut{
			{"e", "Export bookmarks to a new file."},
			{"i", "Choose a file, then preview its bookmark import."},
			{"Space", "In import review: toggle replacement of conflicting records."},
			{"Enter / Esc", "In import review: import / cancel."},
		}},
		{"FORMS & CONFIRMATIONS", []shortcut{
			{"Tab / ↓", "Move to the next field."},
			{"↑", "Move to the previous field; Shift+Tab switches terminal tabs."},
			{"Enter", "Advance a field, then submit or confirm the displayed action."},
			{"Ctrl+U", "Clear the current text field."},
			{"Ctrl+S", "Save an edited connection without opening a session."},
			{"Space", "Toggle the SSO cleanup acknowledgement when focused."},
			{"Esc", "Cancel / go back. Connection drafts are retained."},
		}},
		{"TERMINAL TABS", []shortcut{
			{"Shift+Tab", "Next top-level tab: AWS → shell tabs → AWS."},
			{"Ctrl+]", "Return to the AWS workspace."},
			{"Ctrl+W / Alt+W", "Disconnect and close the current shell tab. A failed local cleanup keeps the tab available."},
			{"Alt+PgUp / PgDn, wheel", "Scroll retained output; any key returns to live output. Inside a full-screen program the gesture goes to the program."},
			{"r / Enter (ended tab)", "Reconnect with the run's frozen credentials and destination; refused once those credentials expire."},
			{"All other keys", "Go to the shell, including Tab, ?, q, and Ctrl+C. Use Ctrl+] first for workspace shortcuts."},
		}},
		{"SIGN-IN & RUNNING OPERATIONS", []shortcut{
			{"o / c", "While a sign-in URL is shown: open it in a browser / copy it."},
			{"Esc", "Request cancellation and wait for cleanup."},
			{"q / Ctrl+C", "Request exit after the running operation finishes cleanup."},
		}},
		{"EXIT WORKSPACE", []shortcut{
			{"q / Ctrl+C", "From the workspace: exit, with confirmation when resources or saved credentials remain."},
			{"Enter", "In exit confirmation: clean up owned sessions and exit; keep credentials."},
			{"c", "In exit confirmation: clear easy-sso managed credentials and exit."},
			{"Esc", "In exit confirmation: return to the workspace."},
		}},
	}
}

func (m *Model) helpContent() string {
	width := m.contentWidth()
	const keyWidth = 18
	separator, rule := " │ ", "─"
	if m.ascii {
		separator, rule = " | ", "-"
	}
	actionWidth := max(1, width-keyWidth-3)
	lines := []string{"Shortcuts by context · uppercase and lowercase keys differ.", "PgUp / PgDn: scroll   ·   ? / Esc: close", "Close Help before using workspace shortcuts. Ctrl+C still requests exit.", ""}
	for _, group := range shortcutGroups() {
		lines = append(lines, m.clickable(group.title, mouseAction{kind: "help-heading"}))
		lines = append(lines, padLine("KEY", keyWidth)+separator+"ACTION", strings.Repeat(rule, width))
		for _, row := range group.rows {
			for i, action := range wrapLines([]string{row.action}, actionWidth) {
				key := ""
				if i == 0 {
					key = row.key
				}
				lines = append(lines, m.clickable(padLine(key, keyWidth)+separator+action, mouseAction{kind: "help-key"}))
			}
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
