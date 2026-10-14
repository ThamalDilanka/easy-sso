package ui

import (
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/lipgloss"
	"strings"
	"testing"
	"time"
)

func TestAllWorkspaceViewsFitAndKeepReviewIdentity(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	c := domain.Connection{ID: "saved", Kind: "ec2", Name: "api-01", AccountID: p.AccountID, Partition: p.Partition, Region: "us-east-2", InstanceID: "i-0123456789abcdef0"}
	f.m.connections = []domain.Connection{c}
	f.m.runs = []domain.Run{{ID: "frozen", Connection: c, Profile: p, State: "Unknown", Title: "api-01 · Example", StartedAt: time.Now()}}
	for _, width := range []int{80, 100, 140} {
		for _, height := range []int{24, 40} {
			f.m.width, f.m.height = width, height
			for _, s := range []screen{ec2, databases, sessions, tunnels, credentials, backup} {
				f.m.changeScreen(s)
				for _, reviewing := range []bool{false, true} {
					if reviewing {
						f.m.connectionReview(c)
					} else {
						f.m.review = nil
					}
					view := f.m.View()
					if !strings.Contains(view, p.AccountID) || !strings.Contains(view, "Role: "+p.Role) || !strings.Contains(view, "SSO source: "+p.SSOSession) {
						t.Fatalf("identity missing at %dx%d: %s", width, height, view)
					}
					rows := strings.Split(view, "\n")
					if len(rows) > height {
						t.Fatalf("height overflow at %dx%d: %d", width, height, len(rows))
					}
					for _, row := range rows {
						if lipgloss.Width(row) > width {
							t.Fatalf("width overflow: %q", row)
						}
					}
				}
			}
		}
	}
}

func TestProfileChangeLeavesNoImplicitTargetOrDraft(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[1]
	frozen := domain.Run{ID: "kept", Profile: f.m.services.Profiles[0], State: "Connected"}
	f.m.runs = []domain.Run{frozen}
	f.sessions.runs = []domain.Run{frozen}
	f.conns.items = []domain.Connection{{ID: "other", Kind: "ec2", AccountID: p.AccountID, Partition: p.Partition}}
	f.m.form = &form{kind: "ec2"}
	f.m.drafts[ec2] = form{kind: "ec2"}
	f.m.review = &review{kind: "connection"}
	f.m.changeProfile(1)
	finish(f.m, f.m.load())
	key(f.m, "enter")
	if f.m.review != nil || f.m.form != nil || len(f.m.drafts) != 0 || f.m.selectedRow != -1 {
		t.Fatal("profile change retained a pending target")
	}
	if len(f.m.runs) != 1 || f.m.runs[0].Profile.Name != frozen.Profile.Name || f.m.runs[0].ID != frozen.ID {
		t.Fatal("profile change retargeted a running session")
	}
	key(f.m, "down")
	key(f.m, "v")
	if f.m.review == nil || f.m.review.connection.ID != "other" {
		t.Fatal("explicit target selection failed")
	}
}

func TestTerminalFocusIsOnlyOfferedForQualifiedAdapter(t *testing.T) {
	f := setupUI(t)
	r := domain.Run{ID: "run", Connection: domain.Connection{Kind: "ec2"}, Title: "api-01 · Example"}
	f.m.review = &review{kind: "run", run: r}
	if strings.Contains(f.m.content(), "Focus terminal") {
		t.Fatal("unqualified focus offered")
	}
	key(f.m, "f")
	if len(f.sessions.focused) != 0 || !strings.Contains(f.m.status, r.Title) {
		t.Fatal("fallback did not retain the exact title")
	}
	f.m.services.FocusTerminal = true
	if !strings.Contains(f.m.content(), "Focus terminal") {
		t.Fatal("qualified focus missing")
	}
	key(f.m, "f")
	if len(f.sessions.focused) != 1 || f.sessions.focused[0] != r.ID {
		t.Fatal("wrong focus identity")
	}
}

func TestHelpAndManualScrollDoNotTriggerUnderlyingActions(t *testing.T) {
	f := setupUI(t)
	f.m.changeScreen(sessions)
	f.m.runs = []domain.Run{{ID: "run", Connection: domain.Connection{Kind: "ec2"}}}
	key(f.m, "?")
	key(f.m, "d")
	if f.m.review != nil {
		t.Fatal("help triggered disconnect review")
	}
	key(f.m, "esc")
	f.m.scroll = 5
	lines := []string{"> selected", "one", "two", "three", "four", "five", "six", "seven"}
	if got := f.m.window(lines, 3, false); got[0] != "five" {
		t.Fatalf("manual scroll pinned to selection: %v", got)
	}
}
