package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/lipgloss"
)

func TestSavedConnectionEnterConnectsAndDetailsNeverConnect(t *testing.T) {
	for _, kind := range []string{"ec2", "db", "iam"} {
		t.Run(kind, func(t *testing.T) {
			f := setupUI(t)
			activateOne(t, f)
			p, _ := f.m.profile()
			c := domain.Connection{ID: "saved", Kind: kind, Name: "target", AccountID: p.AccountID, Partition: p.Partition, Region: p.Region, InstanceID: "i-123", Endpoint: "db.example", DBPort: 5432, LocalPort: 15432}
			f.m.screen, f.m.focus = ec2, 1
			if kind != "ec2" {
				f.m.screen = databases
				c.Kind = "db"
			}
			if kind == "iam" {
				c.AuthMode = "postgres-iam"
				c.Database = "app"
				c.DBUser = "reader"
			}
			f.conns.items = []domain.Connection{c}
			finish(f.m, f.m.load())
			snapshots := f.creds.snapshots
			key(f.m, "c")
			if len(f.sessions.opened) != 0 || f.creds.snapshots != snapshots {
				t.Fatal("removed c shortcut still connects from the saved list")
			}
			f.m.scroll = 12
			key(f.m, "v")
			key(f.m, "c")
			if f.creds.snapshots != snapshots {
				t.Fatal("removed c shortcut still connects from details")
			}
			if f.m.review == nil || !f.m.review.details || f.m.scroll != 0 || len(f.sessions.opened) != 0 {
				t.Fatal("details connected or retained list scroll")
			}
			key(f.m, "esc")
			if f.m.scroll != 12 {
				t.Fatal("back lost list position")
			}
			key(f.m, "enter")
			if len(f.sessions.opened) != 1 || f.m.review != nil {
				t.Fatal("Enter did not connect directly", f.m.status)
			}
		})
	}
}

func TestRunDetailsIndependentOfLongTableAndRefresh(t *testing.T) {
	for _, screen := range []screen{sessions, tunnels} {
		t.Run(screenNames[screen], func(t *testing.T) {
			f := setupUI(t)
			f.m.screen, f.m.focus = screen, 1
			kind := "ec2"
			if screen == tunnels {
				kind = "db"
			}
			now := time.Now()
			for i := 0; i < 70; i++ {
				f.sessions.runs = append(f.sessions.runs, domain.Run{ID: fmt.Sprint(i), StartedAt: now.Add(time.Duration(i) * time.Minute), State: "Listening", Profile: f.m.services.Profiles[1], Connection: domain.Connection{Kind: kind, Name: fmt.Sprintf("target-%02d", i), LocalPort: 15432}})
			}
			finish(f.m, f.m.load())
			if f.m.visibleRuns()[0].ID != "69" {
				t.Fatal("newest run not first")
			}
			if screen == tunnels && len(f.m.visibleRuns()) != 70 {
				t.Fatal("tunnel records hidden")
			}
			f.m.selectedRow = 25
			selected := f.m.visibleRuns()[25]
			f.m.scroll = 30
			key(f.m, "v")
			if f.m.review == nil || f.m.review.run.ID != selected.ID || f.m.scroll != 0 {
				t.Fatal("wrong details or scroll")
			}
			if view := f.m.View(); !strings.Contains(view, selected.Connection.Name) || !strings.Contains(view, "Details /") {
				t.Fatal("details not immediately visible", view)
			}
			// A newly inserted run must not move the selection to a different identity.
			newer := selected
			newer.ID = "new"
			newer.StartedAt = now.Add(100 * time.Minute)
			f.sessions.runs = append(f.sessions.runs, newer)
			finish(f.m, f.m.load())
			key(f.m, "esc")
			if f.m.scroll != 30 || f.m.visibleRuns()[f.m.selectedRow].ID != selected.ID {
				t.Fatal("refresh changed selection")
			}
			if strings.Contains(f.m.runList(f.m.visibleRuns()), "SSM session") {
				t.Fatal("details still appended below table")
			}
			key(f.m, "d")
			if f.m.review == nil || f.m.review.run.ID != selected.ID || f.m.scroll != 0 {
				t.Fatal("stop targeted a different run or scrolled confirmation")
			}
		})
	}
}

func TestRunEnterFocusesOwnedTerminalAndVOnlyInspects(t *testing.T) {
	for _, s := range []screen{sessions, tunnels} {
		f := setupUI(t)
		f.m.screen, f.m.focus = s, 1
		kind := "ec2"
		if s == tunnels {
			kind = "db"
		}
		run := domain.Run{ID: "owned", State: "Connected", Connection: domain.Connection{Kind: kind, Name: "target"}}
		f.m.runs = []domain.Run{run}
		f.m.services.Terminals = &testTerminals{frames: map[string]string{"owned": "prompt"}}
		key(f.m, "v")
		if f.m.review == nil || len(f.m.terminalTabs) != 0 {
			t.Fatal("details focused terminal")
		}
		key(f.m, "esc")
		key(f.m, "enter")
		if len(f.m.terminalTabs) != 1 || f.m.terminalTabs[0].ID != "owned" || f.m.review != nil || len(f.sessions.opened) != 0 {
			t.Fatal("Enter did not focus existing terminal")
		}
	}
}

func TestActionGroupsFitAndRemainClickable(t *testing.T) {
	for _, width := range []int{80, 100, 120, 140, 180} {
		for _, height := range []int{24, 30, 40} {
			for _, kind := range []string{"ec2", "db", "iam", "docdb"} {
				t.Run(fmt.Sprintf("%s/%dx%d", kind, width, height), func(t *testing.T) {
					f := setupUI(t)
					m := f.m
					m.width, m.height = width, height
					m.focus = 1
					p, _ := m.profile()
					c := domain.Connection{ID: "saved", Kind: kind, Name: "selected", AccountID: p.AccountID, Partition: p.Partition, Region: p.Region, Endpoint: "db.example", DBPort: 5432, LocalPort: 15432}
					if kind != "ec2" {
						m.screen = databases
						c.Kind = "db"
					}
					if kind == "docdb" {
						c.Endpoint, c.DBPort, c.LocalPort = "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com", 27017, 37017
					}
					if kind == "iam" {
						c.AuthMode = "postgres-iam"
						c.Database = "app"
						c.DBUser = "reader"
					}
					m.connections = []domain.Connection{c}
					view := m.View()
					if len(strings.Split(view, "\n")) > height {
						t.Fatal("height overflow", view)
					}
					for _, row := range strings.Split(view, "\n") {
						if lipgloss.Width(row) > width {
							t.Fatal("width overflow", row)
						}
					}
					connect := mouseHit(t, m, "key", "enter", 0)
					edit := mouseHit(t, m, "key", "e", 0)
					details := mouseHit(t, m, "key", "v", 0)
					if kind == "docdb" {
						mouseHit(t, m, "key", "c", 0)
						mouseHit(t, m, "key", "l", 0)
					}
					if connect.y > edit.y || edit.y > details.y || (edit.y == details.y && edit.x >= details.x) {
						t.Fatal("inconsistent action order")
					}
					if kind == "ec2" {
						window := mouseHit(t, m, "key", "w", 0)
						if window.y < connect.y || (window.y == connect.y && window.x <= connect.x) {
							t.Fatal("New window not second")
						}
						if width >= 140 && edit.x <= window.x+window.width {
							t.Fatal("secondary actions not separated")
						}
					}
					clickHit(m, details)
					if m.review == nil || !m.review.details || len(f.sessions.opened) != 0 {
						t.Fatal("Details click missed")
					}
					key(m, "esc")
					clickHit(m, mouseHit(t, m, "key", "e", 0))
					if m.form == nil || m.form.original == nil {
						t.Fatal("Edit click missed")
					}
				})
			}
		}
	}
}

func TestLongRunViewsFitAndKeepSelectionAboveTable(t *testing.T) {
	for _, s := range []screen{sessions, tunnels} {
		for _, width := range []int{80, 100, 140} {
			for _, height := range []int{24, 30, 40} {
				f := setupUI(t)
				m := f.m
				m.screen, m.focus = s, 1
				m.width, m.height = width, height
				kind := "ec2"
				if s == tunnels {
					kind = "db"
				}
				m.services.Terminals = &testTerminals{frames: map[string]string{"20": "prompt"}}
				for i := 0; i < 40; i++ {
					m.runs = append(m.runs, domain.Run{ID: fmt.Sprint(i), State: "Connected", Profile: m.services.Profiles[1], Connection: domain.Connection{Kind: kind, Name: fmt.Sprintf("target-%02d", i), LocalPort: 15432}})
				}
				m.selectedRow = 20
				view := m.View()
				if len(strings.Split(view, "\n")) > height {
					t.Fatalf("%s %dx%d overflow: %s", screenNames[s], width, height, view)
				}
				for _, row := range strings.Split(view, "\n") {
					if lipgloss.Width(row) > width {
						t.Fatal("width overflow", row)
					}
				}
				if !strings.Contains(view, "Selected: target-20") || !strings.Contains(view, "Run profile: two") {
					t.Fatal("selection not pinned", view)
				}
				hit := mouseHit(t, m, "key", "v", 0)
				clickHit(m, hit)
				if m.review == nil || m.review.run.ID != "20" || m.scroll != 0 {
					t.Fatal("details target incorrect")
				}
				if !strings.Contains(m.View(), "target-20") {
					t.Fatal("details not visible")
				}
			}
		}
	}
}
