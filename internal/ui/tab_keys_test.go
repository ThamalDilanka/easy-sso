package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

type tabInputCapture struct{ messages []tea.Msg }

func (m *tabInputCapture) Init() tea.Cmd { return nil }
func (m *tabInputCapture) View() string  { return "" }
func (m *tabInputCapture) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.Type == tea.KeyCtrlD {
		return m, tea.Quit
	}
	m.messages = append(m.messages, msg)
	return m, nil
}

func TestShiftTabTerminalInput(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{frames: map[string]string{"alpha": "alpha", "beta": "beta"}}
	f.m.services.Terminals = terminals
	for _, id := range []string{"alpha", "beta"} {
		f.m.showTerminal(domain.Run{ID: id, Connection: domain.Connection{Name: id}, State: "Connected"})
	}
	f.m.selectTab(0)
	capture := &tabInputCapture{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := strings.Repeat("\x1b[Z", 4) + "\t\x03\x1b[200~shift+tab\x1b[201~\x04"
	p := tea.NewProgram(capture, tea.WithInput(strings.NewReader(input)), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, msg := range capture.messages {
		if k, ok := msg.(tea.KeyMsg); ok {
			f.m.Update(msg)
			if k.Type == tea.KeyShiftTab {
				got = append(got, f.m.terminalID)
			}
		}
	}
	if strings.Join(got, ",") != "alpha,beta,,alpha" {
		t.Fatalf("tab sequence: %q", got)
	}
	if f.m.terminalID != "alpha" {
		t.Fatal("ordinary input switched tabs")
	}
	if len(terminals.sent) != 3 || terminals.sent[0] != "alpha:tab" || terminals.sent[1] != "alpha:ctrl+c" {
		t.Fatalf("shell input: %q", terminals.sent)
	}
}

func TestWorkspaceTabCycleAndDirectViewKeys(t *testing.T) {
	f := setupUI(t)
	f.m.screen, f.m.focus = databases, 1
	for _, want := range []struct {
		view  screen
		focus int
	}{{databases, 0}, {ec2, 1}, {databases, 1}, {databases, 0}} {
		key(f.m, "tab")
		if f.m.screen != want.view || f.m.focus != want.focus {
			t.Fatalf("got view=%v focus=%d, want %+v", f.m.screen, f.m.focus, want)
		}
	}
	for _, shortcut := range []string{"3", "4", "5", "6"} {
		key(f.m, shortcut)
		if f.m.screen != screen(shortcut[0]-'1') || f.m.focus != 1 {
			t.Fatal("number key did not focus requested view")
		}
		key(f.m, "tab")
		if f.m.focus != 0 {
			t.Fatal("utility view did not return to Accounts")
		}
		key(f.m, "tab")
		if f.m.screen != ec2 || f.m.focus != 1 {
			t.Fatal("Accounts did not cycle to EC2")
		}
	}
}

func TestViewNavigationGroupsAndUnderlineHitTargets(t *testing.T) {
	f := setupUI(t)
	for _, width := range []int{67, 76, 103} {
		f.m.hits = nil
		lines := f.m.navigation(width, 0, 0)
		if len(lines) != 2 {
			t.Fatalf("width %d: expected label and underline rows", width)
		}
		if !strings.HasPrefix(lines[0], "[1 EC2]") || !strings.HasSuffix(lines[0], "6 Backup") {
			t.Fatalf("groups are not left/right aligned: %q", lines[0])
		}
		for i := range screenNames {
			found := 0
			for _, hit := range f.m.hits {
				if hit.action.kind == "screen" && hit.action.index == i {
					found++
					if hit.y == 1 && strings.TrimSpace(lines[1][hit.x:hit.x+hit.width]) == "" {
						t.Fatal("missing underline")
					}
				}
			}
			if found != 2 {
				t.Fatalf("view %d lacks clickable label and underline", i)
			}
		}
	}
}
