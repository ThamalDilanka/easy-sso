package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/bubbles/timer"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestControlArrowsCycleViewsAndPreserveProfile(t *testing.T) {
	f := setupUI(t)
	profile := f.m.selectedProfile
	for i := 1; i <= len(screenNames); i++ {
		f.m.key(tea.KeyMsg{Type: tea.KeyCtrlRight})
		if f.m.screen != screen(i%len(screenNames)) || f.m.selectedProfile != profile {
			t.Fatal("navigation changed wrong context")
		}
	}
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if f.m.screen != backup {
		t.Fatal("left did not wrap")
	}
	f.m.screen = ec2
	f.m.newForm()
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlRight})
	if f.m.screen != ec2 || f.m.form == nil {
		t.Fatal("editing shortcut changed screen")
	}
	f.m.form = nil
	f.m.review = &review{kind: "exit"}
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlRight})
	if f.m.screen != ec2 {
		t.Fatal("review navigation should stay in review")
	}
}

func TestCredentialTimersStayBoundToExpiry(t *testing.T) {
	f := setupUI(t)
	now := time.Now()
	f.m.roles = []domain.ManagedRole{{Alias: "test", Status: "active", Expiration: now.Add(time.Hour)}}
	if f.m.syncExpiryTimers(now) == nil {
		t.Fatal("timer not started")
	}
	current := f.m.expiryTimers["test"]
	current.expires = now.Add(-time.Second)
	f.m.expiryTimers["test"] = current
	f.m.updateExpiryTimer(timer.TickMsg{ID: current.model.ID()})
	if !f.m.expiryTimers["test"].model.Timedout() {
		t.Fatal("delayed tick extended expiry")
	}
	f.m.roles = nil
	f.m.syncExpiryTimers(now)
	if len(f.m.expiryTimers) != 0 {
		t.Fatal("removed credential retained timer")
	}
}

func TestCredentialsTableFitsAndRetainsRowActions(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	f.m.roles = []domain.ManagedRole{{Alias: "readonly", Profile: p, Status: "active", Expiration: time.Now().Add(time.Hour)}}
	f.creds.roles = f.m.roles
	f.m.screen = credentials
	f.m.selectedRow = 0
	for _, width := range []int{80, 100, 120, 140} {
		f.m.width = width
		f.m.height = 40
		view := f.m.View()
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("overflow at width %d", width)
			}
		}
		for _, label := range []string{"PROFILE", "ACCOUNT", "STATUS", "REMAINING", "Expires:", "Role:"} {
			if !strings.Contains(view, label) {
				t.Fatal("missing", label, width, view)
			}
		}
		clickHit(f.m, mouseHit(t, f.m, "row", "", 0))
		if f.m.selectedRow != 0 {
			t.Fatal("row selection failed")
		}
	}
}

func TestHumanTimestampShowsLocalDateAndUnknown(t *testing.T) {
	value := time.Date(2026, 10, 10, 15, 4, 0, 0, time.Local)
	got := humanTimestamp(value)
	if !strings.Contains(got, "10 Oct 2026, 3:04 PM") {
		t.Fatal(got)
	}
	if humanTimestamp(time.Time{}) != "Unknown" {
		t.Fatal("zero date displayed")
	}
}

func TestManagedLabelsAndSidebarExcludeOnlyOwnedOutputs(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	r := domain.ManagedRole{Alias: "tui-" + p.Name, Profile: p}
	if managedLabel(r) != "easy-sso managed "+p.Name {
		t.Fatal("legacy label leaked")
	}
	r.Alias = "easy-sso-managed-" + p.Name
	if managedLabel(r) != "easy-sso managed "+p.Name {
		t.Fatal("new label incorrect")
	}
	f.m.roles = []domain.ManagedRole{r}
	output := domain.Profile{Name: r.Alias, Error: "sso_session is missing from this AWS profile"}
	unrelated := domain.Profile{Name: "tui-unmanaged", Error: output.Error}
	got := f.m.loginProfiles([]domain.Profile{p, output, unrelated})
	if len(got) != 2 || got[0].Name != p.Name || got[1].Name != unrelated.Name {
		t.Fatal("filter hid wrong profile", got)
	}
}

func TestCredentialsViewShowsPublishedProfileNameAndCopiesIt(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	f.creds.roles = []domain.ManagedRole{{Alias: "easy-sso-managed-one", Profile: p, Status: "active", Expiration: time.Now().Add(time.Hour)}}
	finish(f.m, f.m.load())
	copied := ""
	f.m.services.CopyText = func(text string) error { copied = text; return nil }
	f.m.screen, f.m.focus, f.m.selectedRow = credentials, 1, 0
	view := f.m.View()
	for _, want := range []string{"PROFILE", "easy-sso-managed-one", "Use: --profile easy-sso-managed-one", "Copy profile", "Pause"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	key(f.m, "y")
	if copied != "easy-sso-managed-one" || !strings.Contains(f.m.status, "Copied") {
		t.Fatalf("copied %q status %q", copied, f.m.status)
	}
	copied = ""
	clickHit(f.m, mouseHit(t, f.m, "key", "y", 0))
	if copied != "easy-sso-managed-one" {
		t.Fatal("footer button did not copy")
	}
	f.m.focus = 0
	key(f.m, "y")
	if f.m.form == nil || f.m.form.kind != "alias" {
		t.Fatal("sidebar y lost the account label editor")
	}
}

func TestCopyProfileNameReportsClipboardFailure(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	f.creds.roles = []domain.ManagedRole{{Alias: "easy-sso-managed-one", Profile: p, Status: "active", Expiration: time.Now().Add(time.Hour)}}
	finish(f.m, f.m.load())
	f.m.services.CopyText = func(string) error { return errors.New("no clipboard") }
	f.m.screen, f.m.focus, f.m.selectedRow = credentials, 1, 0
	key(f.m, "y")
	if !strings.Contains(f.m.status, "Could not copy") || !strings.Contains(f.m.status, "easy-sso-managed-one") {
		t.Fatalf("status %q", f.m.status)
	}
}

func TestPauseAndResumeFromCredentialsView(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	f.creds.roles = []domain.ManagedRole{{Alias: "easy-sso-managed-one", Profile: p, Status: "active", Expiration: time.Now().Add(time.Hour)}}
	finish(f.m, f.m.load())
	f.m.screen, f.m.focus, f.m.selectedRow = credentials, 1, 0
	key(f.m, "p")
	if !f.creds.roles[0].Paused || !strings.Contains(f.m.status, "paused") {
		t.Fatalf("pause: roles=%+v status=%q", f.creds.roles, f.m.status)
	}
	view := f.m.View()
	for _, want := range []string{"paused", "Resume"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q after pause:\n%s", want, view)
		}
	}
	if f.m.accountActionLabel() != "Log out" {
		t.Fatal("paused role lost the Log out action")
	}
	clickHit(f.m, mouseHit(t, f.m, "key", "p", 0))
	if f.creds.roles[0].Paused || !strings.Contains(f.m.status, "resumed") {
		t.Fatalf("resume: roles=%+v status=%q", f.creds.roles, f.m.status)
	}
	key(f.m, "p")
	key(f.m, "q")
	if v := f.m.View(); !strings.Contains(v, "paused") {
		t.Fatalf("exit review silent about paused keys:\n%s", v)
	}
}
