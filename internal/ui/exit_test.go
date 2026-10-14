package ui

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
	"github.com/muesli/termenv"
	"reflect"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

func TestExitWithOnlyHistoryRunsCleanupWithoutConfirmation(t *testing.T) {
	f := setupUI(t)
	f.m.runs = []domain.Run{
		{State: "Unknown", Connection: domain.Connection{Kind: "ec2"}},
		{State: "Ended", Connection: domain.Connection{Kind: "ec2"}},
		{State: "Unknown", Connection: domain.Connection{Kind: "db"}},
	}
	cmd := press(f.m, "q")
	if cmd == nil || !f.m.busy {
		t.Fatal("empty workspace waited for confirmation")
	}
	_, quit := f.m.Update(cmd())
	if quit == nil {
		t.Fatal("cleanup did not finish exit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok || f.sessions.shutdowns != 1 {
		t.Fatal("exit skipped cleanup or did not quit")
	}
}

func TestExitConfirmationIsIndependentAndWarnsAboutCredentials(t *testing.T) {
	f := setupUI(t)
	f.m.screen = sessions
	f.m.roles = []domain.ManagedRole{{Alias: "saved"}}
	key(f.m, "q")
	view := f.m.View()
	for _, want := range []string{"Exit workspace?", "saved login credential", "Exit keeps credentials", "Exit (Enter)", "Cancel (Esc)", "Clear creds & exit (c)"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{"Review /", "Sessions", "0 local", "0 owned", "0 independent"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("unexpected %q", unwanted)
		}
	}
	key(f.m, "shift+tab")
	if f.m.terminalID != "" {
		t.Fatal("confirmation switched terminal tabs")
	}
	key(f.m, "esc")
	if f.m.review != nil || f.m.screen != sessions || f.sessions.shutdowns != 0 {
		t.Fatal("cancel changed workspace or shut down")
	}
}

func TestExitWarningsDistinguishOwnershipFromUnverifiedRecords(t *testing.T) {
	f := setupUI(t)
	f.m.services.Terminals = &testTerminals{frames: map[string]string{"hidden": ""}}
	f.m.runs = []domain.Run{
		{ID: "hidden", State: "Unknown", Connection: domain.Connection{Kind: "ec2"}},
		{ID: "stale", State: "Unknown", Connection: domain.Connection{Kind: "ec2"}},
		{ID: "external", State: "Connected", Connection: domain.Connection{Kind: "ec2"}},
		{ID: "recovered", State: "Unknown", Connection: domain.Connection{Kind: "db"}},
	}
	warnings := strings.Join(f.m.exitWarnings(), "\n")
	if !strings.Contains(warnings, "1 local shell") || !strings.Contains(warnings, "1 independent external shell") || strings.Contains(warnings, "owned DB") {
		t.Fatal(warnings)
	}
}

func TestExitCredentialChoices(t *testing.T) {
	for _, clear := range []bool{false, true} {
		f := setupUI(t)
		f.creds.roles = []domain.ManagedRole{{Alias: "first"}, {Alias: "second"}}
		finish(f.m, f.m.load())
		key(f.m, "q")
		choice := "enter"
		if clear {
			choice = "c"
		}
		cmd := press(f.m, choice)
		if cmd == nil {
			t.Fatal("exit button did not start operation")
		}
		_, quit := f.m.Update(cmd())
		if quit == nil {
			t.Fatal("successful exit did not quit")
		}
		if _, ok := quit().(tea.QuitMsg); !ok {
			t.Fatal("unexpected exit result")
		}
		want := 0
		if clear {
			want = 2
		}
		if f.creds.removes != want || len(f.creds.roles) != 2-want || f.sessions.shutdowns != 1 {
			t.Fatal("wrong cleanup for selected exit mode")
		}
	}
}

type blockedRemovalCredentials struct{ *fakeCredentials }

func (c *blockedRemovalCredentials) Remove(string) error {
	return fmt.Errorf("managed profile was externally edited; removal blocked")
}

func TestClearCredentialsFailureKeepsExitCardOpen(t *testing.T) {
	f := setupUI(t)
	f.creds.roles = []domain.ManagedRole{{Alias: "saved"}}
	f.m.services.Credentials = &blockedRemovalCredentials{f.creds}
	finish(f.m, f.m.load())
	key(f.m, "q")
	key(f.m, "c")
	if f.m.busy || f.m.review == nil || len(f.creds.roles) != 1 || !strings.Contains(f.m.View(), "removal blocked") {
		t.Fatal("credential removal failure was hidden")
	}
}

func TestExitCardSolidBackgroundAndButtons(t *testing.T) {
	previous := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previous)
	lipgloss.SetColorProfile(termenv.TrueColor)
	for _, dark := range []bool{true, false} {
		f := setupUI(t)
		f.m.noColor, f.m.ascii, f.m.dark = false, false, dark
		f.creds.roles = []domain.ManagedRole{{Alias: "saved"}}
		finish(f.m, f.m.load())
		key(f.m, "q")
		view := f.m.View()
		screen := vt.NewEmulator(f.m.width, f.m.height)
		screen.Write([]byte(strings.ReplaceAll(view, "\n", "\r\n")))
		p := f.m.palette()
		reference := vt.NewEmulator(2, 1)
		reference.Write([]byte(f.m.paint("C", p.text, p.surface, false) + f.m.paint("B", p.text, p.background, false)))
		inCard := false
		for y, line := range strings.Split(view, "\n") {
			plain := ansi.Strip(line)
			if strings.Index(plain, "╭") == 24 {
				inCard = true
			}
			for x := 0; x < f.m.width; x++ {
				ref := reference.CellAt(1, 0)
				if inCard && x >= 24 && x < 116 {
					ref = reference.CellAt(0, 0)
				}
				if !reflect.DeepEqual(screen.CellAt(x, y).Style.Bg, ref.Style.Bg) {
					t.Fatalf("dark=%v: missing background at %d,%d", dark, x, y)
				}
			}
			if strings.Index(plain, "╰") == 24 {
				inCard = false
			}
		}
		clickHit(f.m, mouseHit(t, f.m, "key", "esc", 0))
		if f.m.review != nil || f.creds.removes != 0 {
			t.Fatal("cancel button changed credentials")
		}
		screen.Close()
		reference.Close()
	}
}
