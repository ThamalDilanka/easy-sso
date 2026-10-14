package ui

import (
	"context"
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"strings"
	"testing"
	"time"
)

type testTerminals struct {
	frames map[string]string
	sent   []string
}

func (t *testTerminals) Has(id string) bool { _, ok := t.frames[id]; return ok }
func (t *testTerminals) Frame(id string, w, h int, focused bool) (string, error) {
	return t.frames[id], nil
}
func (t *testTerminals) Send(id, key, text string, paste bool) error {
	t.sent = append(t.sent, id+":"+key)
	return nil
}

func TestEmbeddedSwitchingAndInputOwnership(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	for _, name := range []string{"alpha", "beta"} {
		c := domain.Connection{ID: name, Name: name, Kind: "ec2", InstanceID: name, Region: "us-east-1", AccountID: p.AccountID, Partition: p.Partition}
		f.conns.items = append(f.conns.items, c)
		f.sessions.runs = append(f.sessions.runs, domain.Run{ID: name, Connection: c, Profile: p, State: "Connected"})
	}
	terminals := &testTerminals{frames: map[string]string{"alpha": "ALPHA OUTPUT", "beta": "BETA OUTPUT"}}
	f.m.services.Terminals = terminals
	finish(f.m, f.m.load())
	key(f.m, "enter")
	if !f.m.terminalFocus || f.m.review != nil || len(f.sessions.opened) != 0 {
		t.Fatal("connect did not focus existing embedded shell")
	}
	if v := f.m.View(); !strings.Contains(v, "ALPHA OUTPUT") || strings.Contains(v, "BETA OUTPUT") {
		t.Fatal("wrong terminal visible")
	}
	for _, width := range []int{100, 140} {
		f.m.width = width
		view := f.m.View()
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("column overflow at width %d", width)
			}
		}
		terminalHit := mouseHit(t, f.m, "terminal", "", 0)
		if terminalHit.width != width-4 {
			t.Fatal("terminal should use the full tab width")
		}
		clickHit(f.m, mouseHit(t, f.m, "tab", "", 0))
		if v := f.m.View(); strings.Contains(v, "ALPHA OUTPUT") || !strings.Contains(v, "Saved instances") {
			t.Fatal("AWS tab did not restore workspace")
		}
		clickHit(f.m, mouseHit(t, f.m, "tab", "", 1))
	}
	f.m.width = 140
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	key(f.m, "q")
	if f.m.review != nil || len(terminals.sent) != 2 {
		t.Fatal("remote keys reached workspace controls")
	}
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
	if f.m.terminalFocus {
		t.Fatal("escape to server list failed")
	}
	key(f.m, "down")
	key(f.m, "enter")
	if v := f.m.View(); !strings.Contains(v, "BETA OUTPUT") || strings.Contains(v, "ALPHA OUTPUT") {
		t.Fatal("switch did not preserve separate screens")
	}
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
	key(f.m, "up")
	key(f.m, "enter")
	if v := f.m.View(); !strings.Contains(v, "ALPHA OUTPUT") {
		t.Fatal("return lost first screen")
	}
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlCloseBracket})
	clickHit(f.m, mouseHit(t, f.m, "key", "v", 0))
	if f.m.review == nil || len(terminals.sent) != 2 {
		t.Fatal("workspace button went to remote shell")
	}
}

func TestSavedEC2ConnectSkipsReview(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	p := f.m.services.Profiles[0]
	f.conns.items = []domain.Connection{{ID: "server", Name: "server", Kind: "ec2", InstanceID: "i-0123456789abcdef0", Region: "us-east-1", AccountID: p.AccountID, Partition: p.Partition}}
	finish(f.m, f.m.load())
	key(f.m, "enter")
	if len(f.sessions.opened) != 1 || f.m.review != nil {
		t.Fatal("saved connection still requires nested review")
	}
}

// Recovered metadata includes shells whose PTY belongs to another process.
type embeddedFocusSessions struct {
	*fakeSessions
	adapter *terminal.Embedded
}

func (s *embeddedFocusSessions) Focus(id string) error {
	s.focused = append(s.focused, id)
	return s.adapter.Focus(id)
}

func TestConnectWithRecoveredExternalShell(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	adapter, err := terminal.NewEmbedded()
	if err != nil {
		t.Skip(err)
	}
	defer adapter.CloseAll()
	f.m.services.Terminals = adapter
	f.m.services.FocusTerminal = true
	f.m.services.Sessions = &embeddedFocusSessions{f.sessions, adapter}
	p := f.m.services.Profiles[0]
	c := domain.Connection{ID: "server", Name: "server", Kind: "ec2", InstanceID: "i-0123456789abcdef0", Region: "us-east-1", AccountID: p.AccountID, Partition: p.Partition}
	f.conns.items = []domain.Connection{c}
	f.sessions.runs = []domain.Run{{ID: "recovered", Connection: c, Profile: p, State: "Connected"}}
	finish(f.m, f.m.load())
	key(f.m, "enter")
	if strings.Contains(f.m.status, "Error:") {
		t.Fatalf("Connect failed: %s", f.m.status)
	}
	if len(f.sessions.opened) != 1 || len(f.sessions.focused) != 0 {
		t.Fatal("connect must open here when the existing terminal belongs elsewhere")
	}
	if len(f.sessions.stopped) != 0 {
		t.Fatal("connect stopped the external shell")
	}
}

func TestConnectPrefersLocalShellOverNewerExternalShell(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	c := domain.Connection{ID: "server", Name: "server", Kind: "ec2", InstanceID: "i-0123456789abcdef0", Region: "us-east-1", AccountID: p.AccountID, Partition: p.Partition}
	f.conns.items = []domain.Connection{c}
	for _, id := range []string{"local", "external"} {
		f.sessions.runs = append(f.sessions.runs, domain.Run{ID: id, Connection: c, Profile: p, State: "Connected"})
	}
	f.m.services.Terminals = &testTerminals{frames: map[string]string{"local": "local shell"}}
	f.m.services.FocusTerminal = true
	finish(f.m, f.m.load())
	key(f.m, "enter")
	if !f.m.terminalFocus || f.m.terminalID != "local" || len(f.sessions.opened) != 0 || len(f.sessions.focused) != 0 {
		t.Fatal("connect did not reuse this workspace's shell")
	}
	f.m.terminalFocus = false
	finish(f.m, f.m.focusRun(f.sessions.runs[1]))
	if len(f.sessions.focused) != 0 || !strings.Contains(f.m.status, "another workspace") {
		t.Fatal("external focus must explain ownership without calling local adapter")
	}
}

func TestTerminalTabsStayBoundAcrossWorkspaceChanges(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	terminals := &testTerminals{frames: map[string]string{"alpha": "ALPHA partial command", "beta": "BETA partial command"}}
	f.m.services.Terminals = terminals
	f.m.services.Sessions = &closingSessions{fakeSessions: f.sessions, terminals: terminals}
	alpha := domain.Run{ID: "alpha", Connection: domain.Connection{Name: "alpha"}, Profile: p, State: "Connected"}
	beta := domain.Run{ID: "beta", Connection: domain.Connection{Name: "beta"}, Profile: p, State: "Connected"}
	f.m.showTerminal(alpha)
	f.m.showTerminal(beta)
	f.m.selectTab(0)
	f.m.changeProfile(1)
	f.m.changeScreen(databases)
	f.m.selectedRow = 3
	// Workspace overlays must not steal input while a terminal tab is selected.
	f.m.help = true
	f.m.selectTab(1)
	f.m.key(tea.KeyMsg{Type: tea.KeyCtrlC})
	if len(terminals.sent) != 1 || terminals.sent[0] != "alpha:ctrl+c" {
		t.Fatal("terminal identity or input ownership changed")
	}
	if v := f.m.View(); !strings.Contains(v, "ALPHA partial command") || strings.Contains(v, "BETA partial command") {
		t.Fatal("wrong screen after profile change")
	}
	f.m.key(tea.KeyMsg{Type: tea.KeyShiftTab})
	if f.m.terminalID != "beta" {
		t.Fatal("next tab failed")
	}
	finish(f.m, f.m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w"), Alt: true}))
	if f.m.terminalID != "" || len(f.m.terminalTabs) != 1 || len(f.sessions.stopped) != 1 || terminals.Has("beta") {
		t.Fatal("closing a tab must stop its session")
	}
	if f.m.selectedProfile != 1 || f.m.screen != databases || !f.m.help {
		t.Fatal("workspace state changed")
	}
	f.m.showTerminal(alpha)
	f.m.showTerminal(alpha)
	if len(f.m.terminalTabs) != 1 {
		t.Fatal("focusing created a duplicate tab")
	}
}

func TestManyLongTerminalTabsRemainReachable(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{frames: map[string]string{}}
	f.m.services.Terminals = terminals
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("run-%d", i)
		terminals.frames[id] = id + " OUTPUT"
		f.m.showTerminal(domain.Run{ID: id, Title: strings.Repeat("長い名前", 20)})
	}
	for _, width := range []int{80, 100, 140} {
		f.m.width = width
		for i := 0; i < 16; i++ {
			f.m.cycleTab(1)
			view := f.m.View()
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("tab overflow: width %d", width)
				}
			}
			if f.m.terminalID != "" {
				active := 0
				for j, r := range f.m.terminalTabs {
					if r.ID == f.m.terminalID {
						active = j + 1
					}
				}
				mouseHit(t, f.m, "tab", "", active)
				if len(strings.Split(view, "\n")) != f.m.height {
					t.Fatal("terminal resize exceeds viewport")
				}
			}
		}
	}
}

func TestNewWindowDoesNotReuseOrMoveLocalShell(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	p := f.m.services.Profiles[0]
	c := domain.Connection{ID: "server", Name: "server", Kind: "ec2", InstanceID: "i-0123456789abcdef0", Region: "us-east-1", AccountID: p.AccountID, Partition: p.Partition}
	f.conns.items = []domain.Connection{c}
	f.sessions.runs = []domain.Run{{ID: "local", Connection: c, Profile: p, State: "Connected"}}
	terminals := &testTerminals{frames: map[string]string{"local": "local screen"}}
	f.m.services.Terminals = terminals
	calls := 0
	f.m.services.OpenWindow = func(ctx context.Context, target domain.Connection, role domain.ManagedRole, snapshot domain.Snapshot) (domain.Run, error) {
		calls++
		if target.ID != c.ID || role.Profile.Name != p.Name {
			t.Fatal("window identity changed")
		}
		return domain.Run{ID: "external", Connection: target, Profile: role.Profile, State: "Connected"}, nil
	}
	finish(f.m, f.m.load())
	key(f.m, "w")
	if calls != 1 || len(f.sessions.opened) != 0 || len(f.sessions.stopped) != 0 || f.m.terminalID != "" || !terminals.Has("local") {
		t.Fatal("window action reused or moved existing shell")
	}
	key(f.m, "enter")
	if f.m.terminalID != "local" {
		t.Fatal("window action changed default connect")
	}
}

func TestWorkspaceWithTerminalActionsFitsViewport(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	f.conns.items = []domain.Connection{{ID: "server", Name: "server", Kind: "ec2", AccountID: p.AccountID, Partition: p.Partition, Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}}
	finish(f.m, f.m.load())
	for _, width := range []int{80, 100, 140} {
		for _, height := range []int{20, 24, 35} {
			f.m.width, f.m.height = width, height
			view := f.m.View()
			if len(strings.Split(view, "\n")) > height {
				t.Fatalf("workspace exceeds %dx%d viewport: %d lines\n%s", width, height, len(strings.Split(view, "\n")), view)
			}
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("workspace line exceeds %d columns", width)
				}
			}
			window := mouseHit(t, f.m, "key", "w", 0)
			newConnection := mouseHit(t, f.m, "key", "n", 0)
			rows := strings.Split(view, "\n")
			titleY := newConnection.y
			if height >= 34 {
				titleY++
			}
			if !strings.Contains(rows[titleY], "Saved instances") {
				t.Fatal("new connection must share the title row")
			}
			detailY := -1
			for y, line := range rows {
				if strings.Contains(line, "i-0123456789abcdef0") {
					detailY = y
					break
				}
			}
			if detailY < 0 || detailY >= window.y {
				t.Fatal("selected instance details must appear before instance actions")
			}
		}
	}
}

func TestConnectButtonShowsSpinnerUntilResult(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	p := f.m.services.Profiles[0]
	f.conns.items = []domain.Connection{{ID: "server", Name: "server", Kind: "ec2", InstanceID: "i-0123456789abcdef0", Region: "us-east-1", AccountID: p.AccountID, Partition: p.Partition}}
	finish(f.m, f.m.load())
	cmd := f.m.connectSelected()
	if cmd == nil || !f.m.busy {
		t.Fatal("connection did not start")
	}
	defer f.m.cancel()
	button := control{"Connect terminal", "enter"}
	first := f.m.buttonLabel(button)
	if strings.Contains(first, "(Enter)") || !strings.Contains(f.m.View(), "Connect terminal") {
		t.Fatal("connect button did not retain a spinner")
	}
	f.m.Update(animationMsg(time.Now()))
	if first == f.m.buttonLabel(button) {
		t.Fatal("button spinner did not animate")
	}
	for _, hit := range f.m.hits {
		if hit.action.kind == "key" && hit.action.key == "enter" {
			t.Fatal("busy connect button still clickable")
		}
	}
	mouseHit(t, f.m, "key", "esc", 0)
	if f.m.key(tea.KeyMsg{Type: tea.KeyEnter}) != nil {
		t.Fatal("duplicate connection started")
	}
	f.m.Update(resultMsg{action: "Open connection", request: f.m.request, epoch: f.m.epoch, err: fmt.Errorf("test connection failure")})
	if f.m.busy || !strings.Contains(f.m.buttonLabel(button), "(Enter)") {
		t.Fatal("spinner not cleared after failure")
	}
}

type closingSessions struct {
	*fakeSessions
	terminals *testTerminals
	closeErr  error
}

func (s *closingSessions) CloseShell(ctx context.Context, id string) error {
	if s.closeErr != nil {
		return s.closeErr
	}
	if err := s.Stop(ctx, id); err != nil {
		return err
	}
	delete(s.terminals.frames, id)
	return nil
}

func TestCtrlWClosesOwnedSessionInsteadOfSendingShellInput(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{frames: map[string]string{"local": "output"}}
	f.m.services.Terminals = terminals
	f.m.services.Sessions = &closingSessions{fakeSessions: f.sessions, terminals: terminals}
	run := domain.Run{ID: "local", State: "Connected", Connection: domain.Connection{Kind: "ec2"}}
	f.sessions.runs = []domain.Run{run}
	f.m.showTerminal(run)
	finish(f.m, f.m.key(tea.KeyMsg{Type: tea.KeyCtrlW}))
	if len(terminals.sent) != 0 || len(f.sessions.stopped) != 1 || len(f.m.terminalTabs) != 0 {
		t.Fatalf("Ctrl+W: sent=%v stopped=%v tabs=%d", terminals.sent, f.sessions.stopped, len(f.m.terminalTabs))
	}
}

func TestCloseFailureKeepsTabVisible(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{frames: map[string]string{"local": "output"}}
	f.m.services.Terminals = terminals
	f.m.services.Sessions = &closingSessions{fakeSessions: f.sessions, terminals: terminals, closeErr: fmt.Errorf("cleanup unconfirmed")}
	f.m.showTerminal(domain.Run{ID: "local", State: "Connected"})
	finish(f.m, f.m.key(tea.KeyMsg{Type: tea.KeyCtrlW}))
	if len(f.m.terminalTabs) != 1 || !terminals.Has("local") || !strings.Contains(f.m.status, "cleanup unconfirmed") {
		t.Fatal("failed close hid session")
	}
}

func TestCloseIconStopsItsOwnTab(t *testing.T) {
	f := setupUI(t)
	terminals := &testTerminals{frames: map[string]string{"one": "first", "two": "second"}}
	f.m.services.Terminals = terminals
	f.m.services.Sessions = &closingSessions{fakeSessions: f.sessions, terminals: terminals}
	for _, id := range []string{"one", "two"} {
		f.m.showTerminal(domain.Run{ID: id, State: "Connected"})
	}
	f.m.View()
	clickHit(f.m, mouseHit(t, f.m, "tab-close", "", 1))
	if len(f.sessions.stopped) != 1 || f.sessions.stopped[0] != "one" || terminals.Has("one") || !terminals.Has("two") {
		t.Fatal("close icon stopped wrong shell")
	}
}
