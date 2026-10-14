package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestProfileLoginStatusTracksUsableKeys(t *testing.T) {
	f := setupUI(t)
	f.m.ascii = false
	p, _ := f.m.profile()
	for _, tc := range []struct {
		status string
		expiry time.Time
		want   string
	}{
		{"active", time.Now().Add(time.Hour), "Active"},
		{"active", time.Now().Add(-time.Hour), "Logged out"},
		{"missing", time.Now().Add(time.Hour), "Logged out"},
		{"unknown", time.Now().Add(time.Hour), "Unknown"},
		{"conflict", time.Now().Add(time.Hour), "Conflict"},
	} {
		f.m.roles = []domain.ManagedRole{{Profile: p, Status: tc.status, Expiration: tc.expiry}}
		if got := f.m.loginBadge(p); got != tc.want {
			t.Fatalf("%s: got %s, want %s", tc.status, got, tc.want)
		}
		sidebar := f.m.sidebar()
		if !strings.Contains(sidebar, p.Name) {
			t.Fatal("profile absent from sidebar")
		}
		if tc.want == "Active" {
			if !strings.Contains(sidebar, "● "+p.Name) {
				t.Fatal("active profile needs filled dot")
			}
		} else {
			if !strings.Contains(sidebar, "○ "+p.Name) {
				t.Fatal("inactive profile needs hollow circle")
			}
		}
		if strings.Contains(sidebar, "Logged out") {
			t.Fatal("logged-out text should not appear in sidebar")
		}
		if (tc.want == "Unknown" || tc.want == "Conflict") && !strings.Contains(sidebar, tc.want) {
			t.Fatal("uncertain status absent from sidebar")
		}
	}
	f.m.roles = nil
	if f.m.loginBadge(p) != "Logged out" {
		t.Fatal("missing keys should show logged out")
	}
}

func TestFooterRightAlignedAndButtonShortcuts(t *testing.T) {
	f := setupUI(t)
	for _, width := range []int{80, 100, 140} {
		f.m.width, f.m.height = width, 40
		view := ansi.Strip(f.m.View())
		if !strings.Contains(view, "New connection (n)") || !strings.Contains(view, "Log in (Ctrl+A)") {
			t.Fatal("missing button shortcuts")
		}
		if strings.Contains(view, "Up/Down") || !strings.Contains(view, "↑/↓") {
			t.Fatal("missing arrow hints")
		}
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, "Ctrl+T: theme") {
				if lipgloss.Width(line) != width || !strings.HasSuffix(line, "Ctrl+T: theme ") {
					t.Fatalf("footer not aligned: %q", line)
				}
			}
		}
	}
}
