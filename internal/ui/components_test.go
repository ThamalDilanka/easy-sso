package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
	"time"
)

func TestExpiryCountdownUsesKnownTime(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		expiry time.Time
		want   string
	}{
		{time.Time{}, "Expiry unknown"},
		{now.Add(-time.Second), "Expired"},
		{now.Add(3661 * time.Second), "Expires in 01:01:01"},
		{now.Add(time.Millisecond), "Expires in 00:00:01"},
	} {
		if got := credentialCountdown(tc.expiry, now); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}

func TestButtonBordersAreClickable(t *testing.T) {
	for _, offset := range []int{0, 1, 2} {
		f := setupUI(t)
		f.m.height = 40
		h := mouseHit(t, f.m, "key", "n", 0)
		h.y += offset
		clickHit(f.m, h)
		if f.m.form == nil {
			t.Fatal("button row did not activate", offset)
		}
		if strings.Contains(f.m.View(), "fields entered") {
			t.Fatal("form progress remains")
		}
	}
}

func TestThemeAndEditorPreserveDraftAndIdentity(t *testing.T) {
	f := setupUI(t)
	m := f.m
	m.newForm()
	m.form.fields[0].value = "api01"
	m.formKey(tea.KeyMsg{Type: tea.KeyLeft})
	m.formKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-")})
	if m.form.fields[0].value != "api0-1" {
		t.Fatal("cursor insertion failed", m.form.fields[0].value)
	}
	oldProfile, oldForm, oldTheme := m.selectedProfile, m.form, m.dark
	var saved string
	m.services.SaveTheme = func(theme string) error { saved = theme; return nil }
	finish(m, m.key(tea.KeyMsg{Type: tea.KeyCtrlT}))
	if m.dark == oldTheme || saved == "" || m.form != oldForm || m.selectedProfile != oldProfile {
		t.Fatal("theme changed context or did not persist")
	}
	m.formKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.form.fields[0].value != "" {
		t.Fatal("clear field failed")
	}
}

func TestStickyHelpAndThemeClickAtEveryWidth(t *testing.T) {
	for _, w := range []int{80, 100, 140} {
		f := setupUI(t)
		m := f.m
		m.width = w
		m.height = 40
		m.status = "Connection status belongs to the right panel"
		rows := strings.Split(ansi.Strip(m.View()), "\n")
		statusRow, helperRow := -1, -1
		for i, row := range rows {
			if strings.Contains(row, "Connection status") {
				statusRow = i
				if w >= 100 && strings.Index(row, "Connection status") < 28 {
					t.Fatal("status in sidebar")
				}
			}
			if strings.Contains(row, "y: edit account label") {
				helperRow = i
			}
		}
		if statusRow < 34 || (w >= 100 && helperRow < 34) {
			t.Fatal("helpers not pinned", w, statusRow, helperRow)
		}
		dark := m.dark
		h := mouseHit(t, m, "theme", "", 0)
		m.mouse(tea.MouseMsg{X: h.x, Y: h.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if m.dark == dark {
			t.Fatal("theme click failed")
		}
	}
}

func TestRapidThemeChangesSaveLatestChoice(t *testing.T) {
	f := setupUI(t)
	m := f.m
	m.dark = false
	saved := []string{}
	m.services.SaveTheme = func(theme string) error { saved = append(saved, theme); return nil }
	first := m.toggleTheme()
	if m.toggleTheme() != nil {
		t.Fatal("concurrent settings save")
	}
	finish(m, first)
	if m.dark || strings.Join(saved, ",") != "dark,light" || m.themeSaving {
		t.Fatal(saved, m.dark, m.themeSaving)
	}
}
