package ui

import (
	"context"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"strings"
	"testing"
)

func TestDatabasePrimaryActionAndHeading(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	p, _ := f.m.profile()
	c := domain.Connection{ID: "db", Kind: "db", Name: "app-db", AccountID: p.AccountID, Partition: p.Partition, Region: p.Region, Endpoint: "db.example.rds.amazonaws.com", DBPort: 5432, LocalPort: 15432, AuthMode: "postgres-iam", Database: "exampledb", DBUser: "reader"}
	f.m.connections = []domain.Connection{c}
	for _, height := range []int{24, 30, 40} {
		f.m.height = height
		f.m.width = 140
		view := f.m.View()
		if len(strings.Split(view, "\n")) > height {
			t.Fatal("database view exceeds screen", height)
		}
		if !strings.Contains(view, "Connect terminal") || !strings.Contains(view, "New connection") {
			t.Fatal("missing database actions")
		}
		if strings.Count(view, "New connection") != 1 {
			t.Fatal("duplicated New connection")
		}
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, "Saved databases") && !strings.Contains(line, "New connection") {
				t.Fatal("New connection not beside heading")
			}
		}
		controls := f.m.controls()
		if controls[0].label != "Connect terminal" || controls[0].key != "enter" {
			t.Fatal("primary action incorrect")
		}
	}
	f.m.editConnection(c)
	if f.m.form.fields[7].value != "postgres-iam" || f.m.form.fields[8].value != "exampledb" || f.m.form.fields[9].value != "reader" {
		t.Fatal("IAM fields not prefilled")
	}
	c.AuthMode = ""
	f.m.connections = []domain.Connection{c}
	f.m.form = nil
	if f.m.controls()[0].label != "Open tunnel" {
		t.Fatal("legacy bookmark pretends to authenticate")
	}
}

func TestLegacyDatabaseOffersIAMSetupWithoutConnecting(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	p, _ := f.m.profile()
	c := domain.Connection{ID: "db", Kind: "db", Name: "Example DB", AccountID: p.AccountID, Partition: p.Partition, Region: p.Region, Endpoint: "db.example.rds.amazonaws.com", LocalPort: 15432, DBPort: 5432}
	f.m.connections = []domain.Connection{c}
	found := false
	for _, control := range f.m.controls() {
		if control.label == "Set up IAM" && control.key == "l" {
			found = true
		}
	}
	if !found {
		t.Fatal("tunnel-only bookmark has no discoverable IAM setup action")
	}
	key(f.m, "l")
	if f.m.form == nil || f.m.form.fields[7].value != "postgres-iam" || f.m.form.index != 8 {
		t.Fatal("IAM setup did not focus the missing database name")
	}
	if f.m.form.original.AuthMode != "" || len(f.sessions.runs) != 0 || f.creds.snapshots != 0 {
		t.Fatal("setup silently changed bookmark or connected")
	}
	content := f.m.content()
	for _, want := range []string{"token automatically", "database username", "rds_iam"} {
		if !strings.Contains(content, want) {
			t.Fatal("missing login instructions", want)
		}
	}
	key(f.m, "esc")
	if f.m.connections[0].AuthMode != "" {
		t.Fatal("cancel enabled IAM")
	}
}

func TestTunnelSuccessDistinguishesTransportFromDatabaseLogin(t *testing.T) {
	f := setupUI(t)
	_, _ = f.m.Update(resultMsg{action: "Open connection", epoch: f.m.epoch, run: domain.Run{State: "Listening", Profile: domain.Profile{Name: "readonly"}, Connection: domain.Connection{Kind: "db", LocalPort: 15432}}})
	for _, want := range []string{"readonly", "database login not attempted", "Set up IAM login"} {
		if !strings.Contains(f.m.status, want) {
			t.Fatal("misleading tunnel success", f.m.status)
		}
	}
}

func TestDatabaseViewShowsTunnelBoundToOtherProfile(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	p, _ := f.m.profile()
	c := domain.Connection{ID: "db", Kind: "db", Name: "Example DB", AccountID: p.AccountID, Partition: p.Partition, Region: p.Region, Endpoint: "db.example.rds.amazonaws.com", DBPort: 5432, LocalPort: 15432}
	f.m.connections = []domain.Connection{c}
	bound := p
	bound.Name = "readonly"
	f.m.runs = []domain.Run{{ID: "owned", Connection: c, Profile: bound, State: "Listening"}}
	for _, height := range []int{24, 40} {
		f.m.height = height
		view := f.m.View()
		if !strings.Contains(view, "readonly") {
			t.Fatal("database view hides tunnel's bound profile", height)
		}
	}
}

func TestRecoveryStatesAreActionableAndHistoricalProfileIsNotShown(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	c := domain.Connection{Kind: "ec2", Name: "instance", LastUsedProfile: "readonly"}
	if details := strings.Join(f.m.connectionDetails(c, p, false), "\n"); strings.Contains(details, "Last profile") || strings.Contains(details, "unverified hint") {
		t.Fatal("historical profile still looks like current identity")
	}
	f.m.screen = tunnels
	runs := []domain.Run{{State: "Needs login"}, {State: "Check failed"}, {State: "Port in use"}, {State: "Remote active"}, {State: "Stopped"}}
	if summary := f.m.sessionSummary(runs); !strings.Contains(summary, "3 need attention") || !strings.Contains(summary, "1 active") || !strings.Contains(summary, "1 history") {
		t.Fatal(summary)
	}
}

type reconcilingSessions struct {
	*fakeSessions
	checks int
}

func (s *reconcilingSessions) Reconcile(context.Context) {
	s.checks++
	s.runs[0].State = "Stopped"
	s.runs[0].RemoteEnded = true
}
func TestWorkspaceRefreshReconcilesRecoveredTunnels(t *testing.T) {
	f := setupUI(t)
	service := &reconcilingSessions{fakeSessions: f.sessions}
	service.runs = []domain.Run{{ID: "old", State: "Checking", Connection: domain.Connection{Kind: "db"}}}
	f.m.services.Sessions = service
	f.m.screen = tunnels
	finish(f.m, f.m.load())
	if service.checks != 1 || len(f.m.runs) != 1 || f.m.runs[0].State != "Stopped" {
		t.Fatal("workspace refresh did not reconcile recovered records")
	}
}
