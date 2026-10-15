package ui

import (
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
