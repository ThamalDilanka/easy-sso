package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

func TestWheelOverTerminalPaneScrollsHistoryNotWorkspace(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{frames: map[string]string{"alpha": "ALPHA"}}
	f.m.services.Terminals = terminals
	f.m.showTerminal(domain.Run{ID: "alpha", Connection: domain.Connection{Name: "alpha", Kind: "ec2"}, State: "Connected"})
	for _, width := range []int{80, 140} {
		f.m.width = width
		f.m.hits = nil
		h := mouseHit(t, f.m, "terminal", "", 0)
		f.m.maxScroll = 100
		finish(f.m, f.m.mouse(tea.MouseMsg{X: h.x + 7, Y: h.y + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}))
		if f.m.scroll != 0 || f.m.sidebarScroll != 0 {
			t.Fatalf("width %d: wheel over the pane scrolled the workspace", width)
		}
		if len(terminals.wheel) == 0 || terminals.wheel[len(terminals.wheel)-1] != "alpha:up:7:2" {
			t.Fatalf("width %d: wheel events %v", width, terminals.wheel)
		}
	}
	key(f.m, "alt+pgup")
	key(f.m, "alt+pgdown")
	if len(terminals.sent) != 0 || len(terminals.scrolled) != 2 || terminals.scrolled[0] <= 0 || terminals.scrolled[1] >= 0 {
		t.Fatalf("page keys: sent=%v scrolled=%v", terminals.sent, terminals.scrolled)
	}
	if v := f.m.View(); !strings.Contains(v, "Alt+PgUp/PgDn: history") {
		t.Fatalf("hint row lacks the history shortcut:\n%s", v)
	}
	terminals.position = [2]int{120, 1820}
	if v := f.m.View(); !strings.Contains(v, "120 of 1820 lines above") {
		t.Fatalf("missing history indicator:\n%s", v)
	}
}

func TestAltCCopiesWholeTerminalForShellAndDatabaseTabs(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{
		frames: map[string]string{"shell": "VISIBLE", "psql": "PSQL", "mongo": "MONGO"},
		text:   map[string]string{"shell": "line 1\nsecret-ish output\nline 3", "psql": "app=> select 1;", "mongo": ""},
	}
	f.m.services.Terminals = terminals
	var copied []string
	f.m.services.CopyText = func(text string) error { copied = append(copied, text); return nil }
	tabs := []domain.Run{
		{ID: "shell", Connection: domain.Connection{Name: "web", Kind: "ec2"}, State: "Connected"},
		{ID: "psql", Connection: domain.Connection{Name: "orders", Kind: "db", AuthMode: "postgres-iam"}, State: "Connected"},
		{ID: "mongo", Connection: domain.Connection{Name: "docs", Kind: "db", AuthMode: "docdb-iam"}, State: "Connected"},
	}
	for _, r := range tabs {
		f.m.showTerminal(r)
	}
	f.m.showTerminal(tabs[0])
	key(f.m, "alt+c")
	if len(copied) != 1 || copied[0] != terminals.text["shell"] {
		t.Fatalf("copied %q", copied)
	}
	if strings.Contains(f.m.status, "secret-ish") || !strings.Contains(f.m.status, "3 lines") {
		t.Fatalf("status %q", f.m.status)
	}
	if len(terminals.sent) != 0 {
		t.Fatalf("Alt+C reached the remote shell: %v", terminals.sent)
	}
	f.m.showTerminal(tabs[1])
	key(f.m, "alt+c")
	if len(copied) != 2 || copied[1] != "app=> select 1;" || !strings.Contains(f.m.status, "1 line ") {
		t.Fatalf("database tab: copied %q status %q", copied, f.m.status)
	}
	f.m.showTerminal(tabs[2])
	key(f.m, "alt+c")
	if len(copied) != 2 || !strings.Contains(f.m.status, "Nothing to copy") {
		t.Fatalf("empty tab: copied %q status %q", copied, f.m.status)
	}
	// An ended SSM tab can still be copied before it is closed.
	terminals.exited = map[string]bool{"shell": true}
	f.m.showTerminal(tabs[0])
	key(f.m, "alt+c")
	if len(copied) != 3 {
		t.Fatalf("ended tab not copied: %q", copied)
	}
	f.m.services.CopyText = func(string) error { return errors.New("no clipboard") }
	key(f.m, "alt+c")
	if !strings.Contains(f.m.status, "Could not copy") {
		t.Fatalf("failure status %q", f.m.status)
	}
	// Ctrl+C still belongs to the shell or client.
	f.m.showTerminal(tabs[1])
	key(f.m, "ctrl+c")
	if len(terminals.sent) != 1 || terminals.sent[0] != "psql:ctrl+c" {
		t.Fatalf("Ctrl+C handling changed: %v", terminals.sent)
	}
}

func TestTerminalHintFitsCopyShortcut(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{frames: map[string]string{"alpha": "ALPHA"}}
	f.m.services.Terminals = terminals
	f.m.showTerminal(domain.Run{ID: "alpha", Connection: domain.Connection{Name: "alpha", Kind: "ec2"}, State: "Connected"})
	f.m.width = 80
	if v := f.m.View(); !strings.Contains(v, "Alt+C: copy all") || !strings.Contains(v, "Alt+PgUp/PgDn: history") || strings.Contains(v, "Shift+Tab: next tab") {
		t.Fatalf("narrow live hint should drop tab shortcuts first:\n%s", v)
	}
	f.m.width = 140
	if v := f.m.View(); !strings.Contains(v, "Shift+Tab: next tab | Ctrl+]: AWS") || !strings.Contains(v, "Alt+C: copy all") {
		t.Fatalf("wide live hint:\n%s", v)
	}
	f.m.width = 80
	terminals.exited = map[string]bool{"alpha": true}
	if v := f.m.View(); !strings.Contains(v, "Alt+C: copy all") {
		t.Fatalf("ended hint:\n%s", v)
	}
	terminals.position = [2]int{12, 300}
	f.m.width = 120
	if v := f.m.View(); !strings.Contains(v, "Alt+C: copy all") {
		t.Fatalf("history hint:\n%s", v)
	}
}
