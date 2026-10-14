package ui

import (
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func mouseHit(t *testing.T, m *Model, kind, key string, index int) hitRegion {
	t.Helper()
	m.View()
	for _, h := range m.hits {
		if h.action.kind == kind && h.action.key == key && h.action.index == index {
			return h
		}
	}
	t.Fatalf("missing mouse target %s/%s/%d\n%s", kind, key, index, m.View())
	return hitRegion{}
}
func clickHit(m *Model, h hitRegion) {
	finish(m, m.mouse(tea.MouseMsg{X: h.x, Y: h.y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}))
}

func TestMouseWrappedNavigationAndControls(t *testing.T) {
	for _, width := range []int{80, 100, 140} {
		f := setupUI(t)
		f.m.width = width
		clickHit(f.m, mouseHit(t, f.m, "screen", "", int(backup)))
		if f.m.screen != backup {
			t.Fatal("wrapped view click missed")
		}
		clickHit(f.m, mouseHit(t, f.m, "key", "e", 0))
		if f.m.form == nil || f.m.form.kind != "export" {
			t.Fatal("export button missed")
		}
		clickHit(f.m, mouseHit(t, f.m, "key", "esc", 0))
		clickHit(f.m, mouseHit(t, f.m, "screen", "", int(ec2)))
		clickHit(f.m, mouseHit(t, f.m, "key", "n", 0))
		if f.m.form == nil || f.m.form.kind != "ec2" {
			t.Fatal("new connection button missed")
		}
		clickHit(f.m, mouseHit(t, f.m, "field", "", 2))
		if f.m.form.index != 2 {
			t.Fatal("field click missed")
		}
	}
}

func TestMouseAccountExpansionNeverActivatesProfile(t *testing.T) {
	f := setupUI(t)
	original, _ := f.m.profile()
	clickHit(f.m, mouseHit(t, f.m, "account", "", 1))
	selected, _ := f.m.profile()
	if selected.Name != original.Name || len(f.creds.selections) > 0 {
		t.Fatal("expanding account activated a profile")
	}
	f.m.review = &review{kind: "connection"}
	clickHit(f.m, mouseHit(t, f.m, "profile", "", 1))
	if f.m.selectedProfile != 1 || f.m.review != nil || f.m.selectedRow != -1 {
		t.Fatal("profile click did not clear pending identity")
	}
}

func TestMouseDuplicateAccountIDsRemainUnderCorrectSource(t *testing.T) {
	f := setupUI(t)
	f.m.services.Profiles = []domain.Profile{{Name: "one", SSOSession: "a", AccountID: "111111111111"}, {Name: "two", SSOSession: "b", AccountID: "111111111111"}}
	// Both accounts have the same rendered ID. Expansion is source-qualified.
	f.m.expanded = map[string]bool{"a/111111111111": false, "b/111111111111": false}
	clickHit(f.m, mouseHit(t, f.m, "account", "", 0))
	if !f.m.accountOpen(f.m.services.Profiles[0]) || f.m.accountOpen(f.m.services.Profiles[1]) {
		t.Fatal("clicked wrong SSO source")
	}
}

func TestMouseScrolledAndTruncatedProfiles(t *testing.T) {
	f := setupUI(t)
	f.m.width = 100
	f.m.height = 24
	f.m.services.Profiles = nil
	for i := 0; i < 35; i++ {
		f.m.services.Profiles = append(f.m.services.Profiles, domain.Profile{Name: fmt.Sprintf("very-long-profile-name-%02d", i), AccountID: "111111111111", SSOSession: "source"})
	}
	f.m.sidebarScroll = 20
	f.m.View()
	var target hitRegion
	found := false
	for _, h := range f.m.hits {
		if h.action.kind == "profile" && h.action.index > 10 {
			target = h
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no scrolled profile target")
	}
	clickHit(f.m, target)
	if f.m.selectedProfile != target.action.index {
		t.Fatal("scrolled hit selected wrong profile")
	}
}

func TestMouseSelectionAndReviewDoNotConnectOrDoubleSubmit(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	f.conns.items = []domain.Connection{{ID: "one", Kind: "ec2", Name: strings.Repeat("long-name", 20), AccountID: p.AccountID, Partition: p.Partition, Region: "us-east-1"}}
	finish(f.m, f.m.load())
	f.m.selectedRow = -1
	f.m.width = 100
	h := mouseHit(t, f.m, "row", "", 0)
	clickHit(f.m, h)
	if f.m.selectedRow != 0 || f.m.review != nil || len(f.sessions.opened) != 0 {
		t.Fatal("row click opened connection")
	}
	clickHit(f.m, mouseHit(t, f.m, "key", "v", 0))
	if f.m.review == nil || len(f.sessions.opened) != 0 {
		t.Fatal("review was bypassed")
	}
	f.m.mouse(tea.MouseMsg{X: h.x, Y: h.y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if len(f.sessions.opened) != 0 {
		t.Fatal("release opened connection")
	}
}

func TestMouseRolePickerAcknowledgementAndBusyGuard(t *testing.T) {
	f := setupUI(t)
	key(f.m, "A")
	clickHit(f.m, mouseHit(t, f.m, "pick", "", 1))
	if !f.m.picked[1] {
		t.Fatal("role checkbox missed")
	}
	clickHit(f.m, mouseHit(t, f.m, "key", "enter", 0))
	clickHit(f.m, mouseHit(t, f.m, "ack", "", 0))
	if !f.m.review.acknowledged {
		t.Fatal("cleanup acknowledgment missed")
	}
	f.m.busy = true
	before := f.m.screen
	clickHit(f.m, mouseHit(t, f.m, "screen", "", int(backup)))
	if f.m.screen != before {
		t.Fatal("mouse switched identity context during operation")
	}
}
