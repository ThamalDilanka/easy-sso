package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/bubbles/timer"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type expiryTimer struct {
	model   timer.Model
	expires time.Time
}

func (m *Model) syncExpiryTimers(now time.Time) tea.Cmd {
	next := map[string]expiryTimer{}
	var cmds []tea.Cmd
	for _, role := range m.roles {
		if !strings.EqualFold(role.Status, "active") || !role.Expiration.After(now) {
			continue
		}
		current, ok := m.expiryTimers[role.Alias]
		if !ok || !current.expires.Equal(role.Expiration) {
			current = expiryTimer{timer.New(role.Expiration.Sub(now)), role.Expiration}
			cmds = append(cmds, current.model.Init())
		}
		next[role.Alias] = current
	}
	m.expiryTimers = next
	if len(cmds) == 0 {
		return nil
	}
	return func() tea.Msg { return tea.BatchMsg(cmds) }
}

func (m *Model) updateExpiryTimer(msg timer.TickMsg) tea.Cmd {
	for alias, current := range m.expiryTimers {
		if current.model.ID() != msg.ID {
			continue
		}
		// Anchor to the actual expiry, so sleeping or a delayed event cannot extend it.
		current.model.Timeout = max(0, time.Until(current.expires)) + current.model.Interval
		var cmd tea.Cmd
		current.model, cmd = current.model.Update(msg)
		m.expiryTimers[alias] = current
		return cmd
	}
	return nil
}

func humanTimestamp(value time.Time) string {
	if value.IsZero() {
		return "Unknown"
	}
	return value.Local().Format("2 Jan 2006, 3:04 PM MST")
}

func (m *Model) identityCredentialStatus(p domain.Profile) string {
	paused := false
	for _, r := range m.roles {
		if sameProfile(r.Profile, p) && roleActive(r) {
			return "active · " + credentialCountdown(r.Expiration, time.Now())
		}
		paused = paused || (sameProfile(r.Profile, p) && rolePaused(r))
	}
	if paused {
		return "paused · resume in Credentials (p)"
	}
	return m.loginStatus(p)
}

// copyProfileName puts the published profile name on the clipboard. Only the
// name is ever copied, never key material.
func (m *Model) copyProfileName(r domain.ManagedRole) tea.Cmd {
	name := r.Alias
	copy := m.copyText()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := copy(ctx, name); err != nil {
			return clipboardMsg{"Could not copy. Use --profile " + name + " (select and copy it manually)."}
		}
		return clipboardMsg{"Copied profile name " + name + " to the clipboard; use it with --profile."}
	}
}

// pauseOrResume toggles whether the role's entry is present in the AWS
// credentials file; the same handler serves the key, the button and the review.
func (m *Model) pauseOrResume(r domain.ManagedRole) tea.Cmd {
	if m.services.Credentials == nil {
		return nil
	}
	action, call := "Pause credentials", m.services.Credentials.Pause
	if rolePaused(r) {
		action, call = "Resume credentials", m.services.Credentials.Resume
	}
	alias := r.Alias
	return m.operation(action, func(context.Context) (domain.Run, domain.ImportPlan, error) {
		return domain.Run{}, domain.ImportPlan{}, call(alias)
	})
}

func (m *Model) credentialsContent() string {
	if len(m.roles) == 0 {
		return "No managed role credentials.\nSelect a profile and press Ctrl+A to log in."
	}
	width := m.contentWidth()
	// Reserve the selection marker, index column, and column spacing.
	indexWidth := max(2, len(fmt.Sprint(len(m.roles))))
	widths := []int{max(6, width-2-6-12-10-10-indexWidth-2), 12, 10, 10}
	row := func(cells ...string) string {
		parts := make([]string, len(cells))
		for i, cell := range cells {
			parts[i] = padLine(ansi.Truncate(safe(cell), widths[i], "…"), widths[i])
		}
		return strings.Join(parts, "  ")
	}
	lines := []string{"  " + padLine("#", indexWidth) + "  " + row("PROFILE", "ACCOUNT", "STATUS", "REMAINING"), "  " + strings.Repeat("-", width-2)}
	for i, r := range m.roles {
		status := managedStatus(r)
		remaining := "—"
		if strings.HasPrefix(status, "active until ") {
			status = "active"
			remaining = strings.TrimPrefix(credentialCountdown(r.Expiration, time.Now()), "Expires in ")
		}
		if m.ascii && remaining == "—" {
			remaining = "-"
		}
		lines = append(lines, m.clickable(m.row(padLine(fmt.Sprint(i+1), indexWidth)+"  "+row(r.Alias, r.Profile.AccountID, status, remaining), i == m.selectedRow), mouseAction{kind: "row", index: i}))
	}
	if m.selectedRow >= 0 && m.selectedRow < len(m.roles) {
		r := m.roles[m.selectedRow]
		lines = append(lines, "", "Expires: "+humanTimestamp(r.Expiration), "Use: --profile "+r.Alias, "Source profile: "+r.Profile.Name, "Account: "+m.accountLabel(r.Profile.AccountID), "Role: "+r.Profile.Role)
	}
	return strings.Join(lines, "\n")
}

func managedLabel(r domain.ManagedRole) string {
	if r.Alias == "tui-"+r.Profile.Name || r.Alias == "easy-sso-managed-"+r.Profile.Name {
		return "easy-sso managed " + r.Profile.Name
	}
	return r.Alias
}
