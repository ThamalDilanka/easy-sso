package ui

import (
	"github.com/charmbracelet/lipgloss"
	"strings"
	"testing"
)

func TestHelpTablesFitAndAllCategoriesAreReachable(t *testing.T) {
	for _, width := range []int{80, 100, 140} {
		f := setupUI(t)
		f.m.width, f.m.height = width, 24
		f.m.scroll = 100
		key(f.m, "?")
		if f.m.scroll != 0 {
			t.Fatal("help opened halfway down")
		}
		var seen strings.Builder
		for page := 0; page < 100; page++ {
			view := f.m.View()
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("help exceeds width %d: %q", width, line)
				}
			}
			if len(strings.Split(view, "\n")) > f.m.height {
				t.Fatalf("help exceeds height at width %d", width)
			}
			seen.WriteString(view)
			if f.m.scroll >= f.m.maxScroll {
				break
			}
			key(f.m, "pgdown")
		}
		for _, want := range []string{"Keyboard help", "WORKSPACE NAVIGATION", "ACCOUNTS & LOGIN", "EC2", "DATABASES", "SESSIONS & TUNNELS", "CREDENTIALS", "BACKUP", "FORMS & CONFIRMATIONS", "TERMINAL TABS", "SIGN-IN & RUNNING OPERATIONS", "EXIT WORKSPACE", "Ctrl+U", "Ctrl+W / Alt+W", "Shift+Tab"} {
			if !strings.Contains(seen.String(), want) {
				t.Errorf("width %d: help section or key unreachable: %q", width, want)
			}
		}
		key(f.m, "q")
		if !f.m.help || f.sessions.shutdowns != 0 {
			t.Fatal("help accidentally executed workspace shortcut")
		}
		key(f.m, "esc")
		if f.m.help {
			t.Fatal("help did not close")
		}
	}
}
