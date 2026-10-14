package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

func TestAccountHeaderLoginLogoutOnlyAffectsSelectedProfile(t *testing.T) {
	f := setupUI(t)
	m := f.m
	for _, c := range m.controls() {
		if c.key == "ctrl+a" {
			t.Fatal("login belongs in account header")
		}
	}
	login := mouseHit(t, m, "key", "ctrl+a", 0)

	navigationY := -1
	for y, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "[1 EC2]") {
			navigationY = y
			break
		}
	}
	if navigationY < 0 || login.y >= navigationY || login.x < m.width/2 {
		t.Fatal("login not on right side of identity header")
	}

	clickHit(m, login)
	p, _ := m.profile()
	if !m.selectedProfileActive() || !strings.Contains(m.View(), "Log out (Ctrl+A)") {
		t.Fatal("header did not change after login")
	}
	other := domain.ManagedRole{Alias: "other", Profile: m.services.Profiles[1], Status: "active", Expiration: time.Now().Add(time.Hour)}
	f.creds.roles = append(f.creds.roles, other)
	finish(m, m.load())
	f.sessions.runs = []domain.Run{{ID: "existing", Profile: p, State: "Connected"}}
	clickHit(m, mouseHit(t, m, "key", "ctrl+a", 0))
	if m.review == nil || m.review.kind != "logout" {
		t.Fatal("missing logout review")
	}
	key(m, "esc")
	if !m.selectedProfileActive() {
		t.Fatal("cancel logged out")
	}
	key(m, "ctrl+a")
	key(m, "enter")
	if m.selectedProfileActive() || len(f.creds.roles) != 1 || f.creds.roles[0].Alias != "other" {
		t.Fatal("logout affected wrong credentials")
	}
	if len(f.sessions.runs) != 1 {
		t.Fatal("logout stopped existing session")
	}
	if !strings.Contains(m.View(), "Log in (Ctrl+A)") {
		t.Fatal("login not restored")
	}
}

func TestReviewRetainsFullEndpointsAndFrozenIdentity(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	c := domain.Connection{Name: "database", Kind: "db", AccountID: p.AccountID, Partition: p.Partition, Endpoint: strings.Repeat("long", 30) + ".rds.amazonaws.com", DBPort: 5432, Region: "eu-west-1", RelayInstanceID: "i-123", RelayRegion: "us-east-1", LocalPort: 15432}
	details := strings.Join(f.m.connectionDetails(c, p, true), "\n")
	for _, value := range []string{c.Endpoint, p.AccountID, p.Role, "DB region", "eu-west-1", "SSM region", "us-east-1", "localhost:15432", "unverified ownership"} {
		if !strings.Contains(details, value) {
			t.Fatal("review lost", value)
		}
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value time.Time
		want  string
	}{
		{time.Time{}, "Never"}, {now.Add(-20 * time.Second), "Just now"}, {now.Add(-time.Minute), "1 minute ago"}, {now.Add(-5 * time.Minute), "5 minutes ago"}, {now.Add(-2 * time.Hour), "2 hours ago"}, {now.Add(-48 * time.Hour), "2 days ago"}, {now.Add(time.Minute), "In 1 minute"},
	} {
		if got := relativeTime(tc.value, now); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}

func TestDetailValuesWrapWithinTheirColumn(t *testing.T) {
	f := setupUI(t)
	for _, ascii := range []bool{true, false} {
		f.m.ascii = ascii
		value := strings.Repeat("long-hostname.", 15)
		row := f.m.detailRow("DB endpoint", value)
		rows := wrapDetailLine(row, 65)
		if len(rows) < 2 {
			t.Fatal("value did not wrap")
		}
		separator := " │ "
		if ascii {
			separator = " | "
		}
		var restored string
		for i, line := range rows {
			if !strings.Contains(line, separator) {
				t.Fatal("value lost separator")
			}
			parts := strings.SplitN(line, separator, 2)
			if len(parts[0]) != 16 {
				t.Fatal("column shifted")
			}
			if i > 0 && strings.TrimSpace(parts[0]) != "" {
				t.Fatal("continuation moved under label")
			}
			restored += parts[1]
		}
		if restored != value {
			t.Fatal("wrapped value lost characters")
		}
	}
}

func TestReviewButtonsKeepBordersAndMouseTargets(t *testing.T) {
	for _, button := range []string{"N", "esc"} {
		for _, offset := range []int{0, 1, 2} {
			f := setupUI(t)
			f.m.height = 40
			f.m.review = &review{kind: "connection", duplicate: true}
			hit := mouseHit(t, f.m, "key", button, 0)
			hit.y += offset
			clickHit(f.m, hit)
			if button == "esc" && f.m.review != nil {
				t.Fatal("cancel border did not work")
			}
			if button == "N" && (f.m.review == nil || f.m.review.duplicate) {
				t.Fatal("new shell border did not work")
			}
		}
	}
}

func TestAccountShortcutRequiresControlAndPreservesShellInput(t *testing.T) {
	f := setupUI(t)
	key(f.m, "a")
	if f.creds.activates != 0 || f.m.review != nil {
		t.Fatal("plain a triggered account action")
	}
	finish(f.m, f.m.key(tea.KeyMsg{Type: tea.KeyCtrlA}))
	if !f.m.selectedProfileActive() {
		t.Fatal("Ctrl+A did not log in")
	}
	key(f.m, "a")
	if f.m.review != nil || !f.m.selectedProfileActive() {
		t.Fatal("plain a triggered logout")
	}
	finish(f.m, f.m.key(tea.KeyMsg{Type: tea.KeyCtrlA}))
	if f.m.review == nil || f.m.review.kind != "logout" || !f.m.selectedProfileActive() {
		t.Fatal("Ctrl+A must review logout before removing keys")
	}
	key(f.m, "esc")
	terminals := &testTerminals{frames: map[string]string{"shell": "prompt"}}
	f.m.services.Terminals = terminals
	f.m.showTerminal(domain.Run{ID: "shell"})
	finish(f.m, f.m.key(tea.KeyMsg{Type: tea.KeyCtrlA}))
	if f.m.review != nil || len(terminals.sent) != 1 || terminals.sent[0] != "shell:ctrl+a" {
		t.Fatal("Ctrl+A in terminal tab must reach the shell")
	}
}

func TestCredentialIndicatorsRespectPartition(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	f.m.selectedProfile = 0
	other := p
	other.Partition = "aws-cn"
	f.m.roles = []domain.ManagedRole{{Alias: "other-partition", Profile: other, Status: "active", Expiration: time.Now().Add(time.Hour)}}
	if f.m.selectedProfileActive() || f.m.roleStatus(p) != "missing" || strings.HasPrefix(f.m.identityCredentialStatus(p), "active") {
		t.Fatal("credentials from another partition appeared active")
	}
}
