package ui

import (
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestSessionsDistinguishesHistoryAndTerminalOwnership(t *testing.T) {
	f := setupUI(t)
	f.m.screen, f.m.focus = sessions, 1
	f.m.width, f.m.height = 160, 45
	f.m.services.Terminals = &testTerminals{frames: map[string]string{"local": "", "hidden": ""}}
	for _, item := range []struct{ id, state string }{
		{"ended", "Ended"}, {"failed", "Failed"}, {"uncertain", "Unknown"},
		{"external", "Connected"}, {"local", "Connected"}, {"hidden", "Connected"},
	} {
		f.sessions.runs = append(f.sessions.runs, domain.Run{ID: item.id, State: item.state, StartedAt: time.Date(2026, 10, 11, 14, 30, 0, 0, time.Local), Connection: domain.Connection{Kind: "ec2", Name: item.id}})
	}
	finish(f.m, f.m.load())
	f.m.showTerminal(f.m.runs[4])
	f.m.selectTab(0)
	view := f.m.View()
	for _, want := range []string{"Sessions / all accounts", "11 Oct 2026 14:30", "STARTED (LOCAL)", "Unverified", "3 active", "1 unverified", "2 history", "1 open tab", "Hidden tab", "No local tab"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in Sessions view:\n%s", want, view)
		}
	}
	for i, want := range []string{"History", "History", "No local tab", "No local tab", "Open tab", "Hidden tab"} {
		if got := f.m.runTerminal(f.m.runs[i]); got != want {
			t.Errorf("terminal %d = %q, want %q", i, got, want)
		}
	}
	if len(f.m.visibleRuns()) != 6 || len(f.m.terminalTabs) != 1 || len(f.sessions.opened) != 0 {
		t.Fatal("view hid records or created shells/tabs")
	}
}

func TestSessionsRetainedRecordsWithoutTabs(t *testing.T) {
	f := setupUI(t)
	f.m.screen, f.m.focus = sessions, 1
	f.m.width, f.m.height = 160, 45
	for i := 0; i < 21; i++ {
		state := "Ended"
		if i >= 16 {
			state = "Unknown"
		}
		f.sessions.runs = append(f.sessions.runs, domain.Run{State: state, Connection: domain.Connection{Kind: "ec2", Name: "retained"}})
	}
	finish(f.m, f.m.load())
	view := f.m.View()
	for _, want := range []string{"0 active", "5 unverified", "16 history", "0 open tabs"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(view, "Open shells") || len(f.m.terminalTabs) != 0 || len(f.sessions.opened) != 0 {
		t.Fatal("history presented as open shells")
	}
}
