package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type fakeCredentials struct {
	roles                                  []domain.ManagedRole
	selections                             []domain.Selection
	activates, removes, retries, snapshots int
	fail                                   error
}

func (f *fakeCredentials) List() ([]domain.ManagedRole, error) {
	return append([]domain.ManagedRole(nil), f.roles...), nil
}
func (f *fakeCredentials) Activate(_ context.Context, s []domain.Selection, ack bool) ([]domain.ManagedRole, error) {
	f.activates++
	if !ack {
		return nil, errors.New("cleanup acknowledgment required")
	}
	if f.fail != nil {
		return nil, f.fail
	}
	f.selections = append([]domain.Selection(nil), s...)
	for _, sel := range s {
		found := false
		role := domain.ManagedRole{Alias: sel.Alias, Profile: sel.Profile, Expiration: time.Now().Add(time.Hour), Status: "active"}
		for i, r := range f.roles {
			if r.Alias == sel.Alias {
				f.roles[i] = role
				found = true
			}
		}
		if !found {
			f.roles = append(f.roles, role)
		}
	}
	return f.roles, nil
}
func (f *fakeCredentials) Snapshot(alias string) (domain.ManagedRole, domain.Snapshot, error) {
	f.snapshots++
	for _, r := range f.roles {
		if r.Alias == alias {
			if !r.Expiration.After(time.Now()) || r.Status == "unknown" {
				return domain.ManagedRole{}, domain.Snapshot{}, errors.New("credentials expired or unknown")
			}
			return r, domain.Snapshot{AccessKeyID: "SECRET_ACCESS_TEST_ONLY", SecretAccessKey: "SECRET_TEST_ONLY", SessionToken: "TOKEN_TEST_ONLY", Expiration: r.Expiration}, nil
		}
	}
	return domain.ManagedRole{}, domain.Snapshot{}, errors.New("missing role")
}
func (f *fakeCredentials) Remove(alias string) error {
	f.removes++
	for i, r := range f.roles {
		if r.Alias == alias {
			f.roles = append(f.roles[:i], f.roles[i+1:]...)
			return nil
		}
	}
	return errors.New("not owned")
}
func (f *fakeCredentials) RetryCleanup(context.Context) error { f.retries++; f.fail = nil; return nil }
func (f *fakeCredentials) Pause(alias string) error           { return f.setPaused(alias, true) }
func (f *fakeCredentials) Resume(alias string) error          { return f.setPaused(alias, false) }
func (f *fakeCredentials) setPaused(alias string, paused bool) error {
	if f.fail != nil {
		return f.fail
	}
	for i, r := range f.roles {
		if r.Alias == alias {
			f.roles[i].Paused = paused
			f.roles[i].Status = map[bool]string{true: "paused", false: "active"}[paused]
			return nil
		}
	}
	return errors.New("not owned")
}

type fakeConnections struct {
	items             []domain.Connection
	plan              domain.ImportPlan
	exported          string
	previews, commits int
	replaced          bool
	fail              error
}

func (f *fakeConnections) List() ([]domain.Connection, error) {
	return append([]domain.Connection(nil), f.items...), nil
}
func (f *fakeConnections) Save(c domain.Connection) error { f.items = append(f.items, c); return nil }
func (f *fakeConnections) Export(path string) error       { f.exported = path; return f.fail }
func (f *fakeConnections) Preview(string) (domain.ImportPlan, error) {
	f.previews++
	return f.plan, f.fail
}
func (f *fakeConnections) Commit(p domain.ImportPlan, replace bool) error {
	f.commits++
	f.replaced = replace
	f.items = append(f.items, p.Additions...)
	return f.fail
}

type fakeSessions struct {
	runs                        []domain.Run
	opened                      []domain.Connection
	stopped, focused, rechecked []string
	reopened                    []string
	reopenRun                   domain.Run
	reopenErr                   error
	shutdowns                   int
	fail                        error
	connections                 *fakeConnections
}

func (f *fakeSessions) List() []domain.Run { return append([]domain.Run(nil), f.runs...) }
func (f *fakeSessions) Reopen(_ context.Context, id string) (domain.Run, error) {
	f.reopened = append(f.reopened, id)
	return f.reopenRun, f.reopenErr
}
func (f *fakeSessions) Open(_ context.Context, c domain.Connection, r domain.ManagedRole, _ domain.Snapshot) (domain.Run, error) {
	if f.fail != nil {
		return domain.Run{}, f.fail
	}
	f.opened = append(f.opened, c)
	state := "Connected"
	if c.Kind == "db" {
		state = "Listening"
	}
	run := domain.Run{ID: fmt.Sprintf("run-%d", len(f.runs)+1), Connection: c, Profile: r.Profile, Alias: r.Alias, State: state, SessionID: "exact-session", Title: c.Name + " · selected account", StartedAt: time.Now()}
	f.runs = append(f.runs, run)
	if f.connections != nil {
		f.connections.Save(c)
	}
	return run, nil
}
func (f *fakeSessions) Stop(_ context.Context, id string) error {
	f.stopped = append(f.stopped, id)
	for i, r := range f.runs {
		if r.ID == id {
			f.runs[i].State = "Ended"
		}
	}
	return nil
}
func (f *fakeSessions) Focus(id string) error {
	f.focused = append(f.focused, id)
	return errors.New("Terminal focus unavailable. Switch using the terminal application.")
}
func (f *fakeSessions) Recheck(_ context.Context, id string) error {
	f.rechecked = append(f.rechecked, id)
	return nil
}
func (f *fakeSessions) OwnsTunnel(id string) bool {
	for _, run := range f.runs {
		if run.ID == id && run.Connection.Kind == "db" && run.State != "Unknown" {
			return true
		}
	}
	return false
}
func (f *fakeSessions) Shutdown(context.Context) error {
	f.shutdowns++
	for i, r := range f.runs {
		if r.Connection.Kind == "db" {
			f.runs[i].State = "Stopped"
		}
	}
	return f.fail
}

type fixture struct {
	m        *Model
	creds    *fakeCredentials
	conns    *fakeConnections
	sessions *fakeSessions
}

func setupUI(t *testing.T) fixture {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	t.Setenv("EASY_SSO_ASCII", "1")
	profiles := []domain.Profile{{Name: "one", SSOSession: "login", AccountID: "111111111111", Role: "ReadOnlyAccess", Partition: "aws", Region: "us-east-1", DirectSSO: true}, {Name: "two", SSOSession: "login", AccountID: "222222222222", Role: "OrganizationAccountAccessRole", Partition: "aws", Region: "eu-west-1", Chain: []string{"one", "two"}}}
	c := &fakeCredentials{}
	b := &fakeConnections{}
	s := &fakeSessions{connections: b}
	m := New(domain.Services{Profiles: profiles, Credentials: c, Connections: b, Sessions: s, Prerequisites: domain.Prerequisites{Ready: true, CLIPath: "fake-aws", PluginPath: "fake-plugin", StateDir: "/test-state"}, AccountAliases: map[string]string{"111111111111": "First account"}}).(*Model)
	m.width = 140
	m.height = 40
	finish(m, m.load())
	return fixture{m, c, b, s}
}
func finish(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg.(type) {
	case tea.BatchMsg:
		return
	case tea.QuitMsg:
		return
	}
	_, next := m.Update(msg)
	if next != nil {
		finish(m, next)
	}
}
func press(m *Model, key string) tea.Cmd {
	var k tea.KeyMsg
	switch key {
	case "enter":
		k.Type = tea.KeyEnter
	case "esc":
		k.Type = tea.KeyEsc
	case "tab":
		k.Type = tea.KeyTab
	case "shift+tab":
		k.Type = tea.KeyShiftTab
	case "up":
		k.Type = tea.KeyUp
	case "down":
		k.Type = tea.KeyDown
	case "ctrl+c":
		k.Type = tea.KeyCtrlC
	case "ctrl+u":
		k.Type = tea.KeyCtrlU
	case "pgdown":
		k.Type = tea.KeyPgDown
	case "pgup":
		k.Type = tea.KeyPgUp
	case "alt+pgup":
		k = tea.KeyMsg{Type: tea.KeyPgUp, Alt: true}
	case "alt+pgdown":
		k = tea.KeyMsg{Type: tea.KeyPgDown, Alt: true}
	default:
		k = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	_, cmd := m.Update(k)
	return cmd
}
func key(m *Model, s string) { finish(m, press(m, s)) }
func activateOne(t *testing.T, f fixture) {
	t.Helper()
	before := f.creds.activates
	key(f.m, "A")
	key(f.m, "enter")
	key(f.m, "enter")
	key(f.m, " ")
	key(f.m, "enter")
	if f.creds.activates != before+1 {
		t.Fatalf("activation did not run: %s", f.m.View())
	}
}
func fill(m *Model, values ...string) {
	for _, v := range values {
		key(m, "ctrl+u")
		key(m, v)
		key(m, "enter")
	}
}

func TestMultiRoleActivationReviewAcknowledgmentAndAliasEditing(t *testing.T) {
	f := setupUI(t)
	m := f.m
	key(m, "A")
	key(m, "down")
	key(m, " ")
	key(m, "enter")
	view := m.View()
	for _, want := range []string{"111111111111", "222222222222", "OrganizationAccountAccessRole", "all profiles in this environment"} {
		if !strings.Contains(view, want) {
			t.Fatalf("review missing %q: %s", want, view)
		}
	}
	key(m, "ctrl+u")
	key(m, "alternate-one")
	key(m, "enter")
	key(m, "enter")
	key(m, "enter")
	if f.creds.activates != 0 {
		t.Fatal("activation began without acknowledgment")
	}
	key(m, " ")
	cmd := press(m, "enter")
	if f.creds.activates != 0 {
		t.Fatal("service ran synchronously in Update")
	}
	finish(m, cmd)
	if len(f.creds.selections) != 2 || f.creds.selections[0].Alias != "alternate-one" {
		t.Fatalf("wrong selection %#v", f.creds.selections)
	}
	if !strings.Contains(m.View(), "SSO cleanup complete") {
		t.Fatal(m.View())
	}
	if strings.Contains(m.View(), "SECRET_TEST_ONLY") {
		t.Fatal("secret rendered")
	}
}

func TestActivationFailureRetainsEditableReviewAndRecovery(t *testing.T) {
	f := setupUI(t)
	f.creds.fail = errors.New("SSO cleanup failed. New credentials were not published. Retry cleanup.")
	key(f.m, "A")
	key(f.m, "enter")
	key(f.m, "enter")
	key(f.m, " ")
	key(f.m, "enter")
	if len(f.creds.roles) != 0 || f.m.review == nil || !strings.Contains(f.m.status, "not published") {
		t.Fatal("failure lost safe review or claimed success")
	}
	key(f.m, "esc")
	key(f.m, "5")
	key(f.m, "c")
	if f.creds.retries != 1 {
		t.Fatal("cleanup retry missing")
	}
	activateOne(t, f)
	if len(f.creds.roles) != 1 {
		t.Fatal("explicit renewal did not publish")
	}
}

func TestEC2JourneyDuplicateFocusAndTargetedDisconnect(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	key(f.m, "n")
	fill(f.m, "example-instance", "i-0123456789abcdef0", "us-east-1")
	if f.m.review == nil || len(f.sessions.opened) != 0 {
		t.Fatal("new target skipped review")
	}
	key(f.m, "enter")
	if len(f.sessions.opened) != 1 || len(f.conns.items) != 1 {
		t.Fatal("successful connection missing")
	}
	key(f.m, "enter")
	if len(f.sessions.opened) != 1 || f.m.review != nil {
		t.Fatal("direct connect must reuse existing shell")
	}
	key(f.m, "v")
	if !f.m.review.duplicate {
		t.Fatal("existing shell not detected")
	}
	key(f.m, "enter")
	if len(f.sessions.opened) != 1 {
		t.Fatal("implicit duplicate")
	}
	key(f.m, "f")
	if len(f.sessions.opened) != 1 || len(f.sessions.focused) != 0 || !strings.Contains(f.m.status, "Switch to the separate terminal window") {
		t.Fatal("focus launched duplicate or overstated support")
	}
	key(f.m, "N")
	key(f.m, "enter")
	if len(f.sessions.runs) != 2 {
		t.Fatal("explicit second shell missing")
	}
	key(f.m, "3")
	key(f.m, "d")
	key(f.m, "enter")
	if len(f.sessions.stopped) != 1 || f.sessions.stopped[0] != f.sessions.runs[1].ID || f.sessions.runs[1].State != "Ended" || f.sessions.runs[0].State != "Connected" {
		t.Fatal("disconnect affected other shell")
	}
	key(f.m, "r")
	if len(f.sessions.rechecked) != 1 {
		t.Fatal("bounded recheck unavailable")
	}
}

func TestDatabaseRegionsPortReviewAndNormalExit(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	key(f.m, "2")
	key(f.m, "n")
	fill(f.m, "app-db", "DB.EXAMPLE.RDS.AMAZONAWS.COM", "eu-west-1", "5432", "i-0123456789abcdef0", "us-east-1", "15432", "tunnel", "", "", "")
	v := f.m.View()
	for _, want := range []string{"DB region", "eu-west-1", "SSM region", "us-east-1", "unverified ownership", "localhost:15432"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %s: %s", want, v)
		}
	}
	key(f.m, "p")
	fill(f.m, "443")
	if f.m.form == nil || !strings.Contains(f.m.status, "1024") {
		t.Fatal("invalid local port accepted")
	}
	fill(f.m, "25432")
	if f.m.review.connection.LocalPort != 25432 {
		t.Fatal("local port edit lost")
	}
	key(f.m, "enter")
	if !strings.Contains(f.m.status, "Tunnel open at localhost:25432") || strings.Contains(f.m.status, "authenticated") {
		t.Fatal(f.m.status)
	}
	if f.sessions.opened[0].Endpoint != "db.example.rds.amazonaws.com" {
		t.Fatal("endpoint not normalized")
	}
	shell := domain.Run{ID: "independent-shell", Connection: domain.Connection{Kind: "ec2"}, Profile: f.m.services.Profiles[0], State: "Connected"}
	f.sessions.runs = append(f.sessions.runs, shell)
	finish(f.m, f.m.load())
	key(f.m, "q")
	if !strings.Contains(f.m.View(), "1 owned DB tunnel") || !strings.Contains(f.m.View(), "1 independent external shell") {
		t.Fatal(f.m.View())
	}
	key(f.m, "enter")
	if f.sessions.shutdowns != 1 || f.sessions.runs[0].State != "Stopped" || f.sessions.runs[1].State != "Connected" {
		t.Fatal("exit lifecycle incorrect")
	}
}

func TestContextChangeClearsApprovalAndDraft(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	key(f.m, "n")
	key(f.m, "draft name")
	key(f.m, "esc")
	key(f.m, "tab")
	key(f.m, "tab")
	key(f.m, "down")
	p, _ := f.m.profile()
	if p.AccountID != "222222222222" {
		t.Fatal("profile selection wrong")
	}
	key(f.m, "tab")
	key(f.m, "n")
	if f.m.form.fields[0].value != "" {
		t.Fatal("draft crossed a profile boundary")
	}
	fill(f.m, "different-context", "i-0123456789abcdef0", "eu-west-1")
	f.m.changeProfile(0)
	if f.m.review != nil || f.m.selectedRow != -1 {
		t.Fatal("pending account approval carried over")
	}
}

func TestStaleAsyncResultCannotEnableAnotherAccount(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	c := domain.Connection{Kind: "ec2", Name: "api", AccountID: "111111111111", Partition: "aws", Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}
	f.m.connectionReview(c)
	cmd := press(f.m, "enter")
	if len(f.sessions.opened) != 0 {
		t.Fatal("sync operation")
	}
	f.m.changeProfile(1)
	msg := cmd()
	f.m.Update(msg)
	if f.m.review != nil || f.m.selectedProfile != 1 || strings.Contains(f.m.status, "Connected") {
		t.Fatal("stale approval changed context")
	}
	epoch := f.m.epoch
	f.m.Update(loadedMsg{epoch: epoch - 1, connections: []domain.Connection{c}})
	if len(f.m.connections) != 0 {
		t.Fatal("stale list applied")
	}
	f.m.Update(resultMsg{request: f.m.request - 1, epoch: epoch, action: "Import preview"})
	if f.m.review != nil {
		t.Fatal("old request enabled review")
	}
}

func TestImportPreviewExplicitReplacementAndNoCredentials(t *testing.T) {
	f := setupUI(t)
	f.conns.plan = domain.ImportPlan{Additions: []domain.Connection{{Name: "imported", AccountID: "111111111111", Region: "eu-west-1"}}, Conflicts: []domain.Connection{{Name: "existing", AccountID: "111111111111", Region: "us-east-1"}}, Unbound: []string{"missing-profile"}}
	key(f.m, "6")
	key(f.m, "i")
	key(f.m, "enter")
	if f.m.review == nil || f.conns.commits != 0 {
		t.Fatal("import skipped preview")
	}
	view := f.m.View()
	for _, want := range []string{"Additions: 1", "Conflicts: 1", "missing-profile", "Keep local conflicts"} {
		if !strings.Contains(view, want) {
			t.Fatal(view)
		}
	}
	key(f.m, "enter")
	if f.conns.replaced || f.creds.activates != 0 || len(f.sessions.opened) != 0 {
		t.Fatal("import activated or connected")
	}
	key(f.m, "i")
	key(f.m, "enter")
	key(f.m, " ")
	key(f.m, "enter")
	if !f.conns.replaced {
		t.Fatal("explicit replacement ignored")
	}
	key(f.m, "e")
	fill(f.m, "/chosen/backup.json")
	if f.conns.exported != "/chosen/backup.json" {
		t.Fatal("export path ignored")
	}
}

func TestCredentialRemovalRenewalAndExpiryDoNotRetargetRuns(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	p, _ := f.m.profile()
	f.sessions.runs = []domain.Run{{ID: "existing", Profile: p, Connection: domain.Connection{Kind: "ec2"}, State: "Connected"}}
	finish(f.m, f.m.load())
	key(f.m, "5")
	key(f.m, "r")
	key(f.m, "enter")
	key(f.m, " ")
	key(f.m, "enter")
	if f.creds.activates != 2 {
		t.Fatal("renew did not repeat activation")
	}
	key(f.m, "d")
	if f.creds.removes != 0 {
		t.Fatal("removed without review")
	}
	key(f.m, "enter")
	if f.creds.removes != 1 || f.sessions.runs[0].State != "Connected" {
		t.Fatal("removal revoked existing run")
	}
	f.creds.roles = []domain.ManagedRole{{Alias: "expired", Profile: p, Expiration: time.Now().Add(-time.Minute), Status: "active"}}
	finish(f.m, f.m.load())
	key(f.m, "1")
	f.m.connectionReview(domain.Connection{Kind: "ec2", AccountID: p.AccountID, Partition: p.Partition})
	key(f.m, "enter")
	if len(f.sessions.opened) != 0 || !strings.Contains(f.m.status, "Warning: Selected account "+p.AccountID) || !strings.Contains(f.m.status, "Log in with Ctrl+A") {
		t.Fatal("expired credentials launched")
	}
}

func TestTextEditingSuppressesGlobalShortcutsAndSanitizesPaste(t *testing.T) {
	f := setupUI(t)
	key(f.m, "n")
	key(f.m, "aqns123456?")
	if f.m.screen != ec2 || f.m.review != nil || f.m.picker || f.m.help || f.m.form.fields[0].value != "aqns123456?" {
		t.Fatal("shortcut fired while editing")
	}
	key(f.m, "\x1b[31m\x00test\n")
	if strings.Contains(f.m.form.fields[0].value, "\x1b") || strings.Contains(f.m.View(), "\x00") {
		t.Fatal("control sequence retained")
	}
	key(f.m, "esc")
	if len(f.sessions.stopped) != 0 {
		t.Fatal("escape disconnected session")
	}
	key(f.m, "ctrl+c")
	if f.m.review == nil || f.m.review.kind != "exit" {
		t.Fatal("Ctrl+C skipped exit flow")
	}
}

func TestResponsiveIdentityAndFocusedActivationControls(t *testing.T) {
	f := setupUI(t)
	key(f.m, "A")
	key(f.m, "down")
	key(f.m, " ")
	key(f.m, "enter")
	key(f.m, "tab")
	key(f.m, "tab")
	for _, width := range []int{80, 100, 140} {
		for _, theme := range []bool{false, true} {
			f.m.width = width
			f.m.height = 24
			f.m.dark = theme
			view := f.m.View()
			if !strings.Contains(view, "111111111111") || !strings.Contains(view, "ReadOnlyAccess") || !strings.Contains(view, "Acknowledge cleanup") {
				t.Fatalf("width %d missing identity/field: %s", width, view)
			}
			if strings.Contains(view, "\x1b") {
				t.Fatal("NO_COLOR produced escape sequence")
			}
			for _, line := range strings.Split(view, "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("width %d overflow %d: %q", width, lipgloss.Width(line), line)
				}
			}
		}
	}
	f.m.width = 35
	v := f.m.View()
	if !strings.Contains(v, "111111111111") || !strings.Contains(v, "Resize") {
		t.Fatal("resize state hid identity")
	}
}

func TestMissingPrerequisitesAndUnknownStatesAreHonest(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	f.m.services.Prerequisites.Ready = false
	f.m.services.Prerequisites.Problems = []string{"Install AWS CLI v2 and Session Manager plugin"}
	f.m.connectionReview(domain.Connection{Kind: "ec2", AccountID: "111111111111", Partition: "aws"})
	key(f.m, "enter")
	if len(f.sessions.opened) != 0 || !strings.Contains(f.m.status, "prerequisites unavailable") {
		t.Fatal("missing prerequisites allowed launch")
	}
	f.m.review = nil
	f.m.changeScreen(sessions)
	f.m.runs = []domain.Run{{ID: "unknown", Profile: f.m.services.Profiles[0], Connection: domain.Connection{Kind: "ec2"}, State: "Unknown", StartedAt: time.Now()}}
	if !strings.Contains(f.m.View(), "Unverified") {
		t.Fatal("unknown rendered as connected")
	}
	f.m.Update(AuthNoticeMsg("Open https://device.example/ and use code TEST-CODE"))
	if !strings.Contains(f.m.status, "TEST-CODE") {
		t.Fatal("official login notice lost")
	}
}

func TestAliasLabelsDoNotChangeAccountBindingsAndProfilesGroup(t *testing.T) {
	f := setupUI(t)
	var savedID, savedLabel string
	f.m.services.SaveAccountAlias = func(id, label string) error { savedID = id; savedLabel = label; return nil }
	key(f.m, "y")
	fill(f.m, "Edited label")
	if savedID != "111111111111" || savedLabel != "Edited label" {
		t.Fatal("alias persistence wrong")
	}
	p, _ := f.m.profile()
	if p.AccountID != "111111111111" || !strings.Contains(f.m.View(), "Edited label") {
		t.Fatal("label altered identity")
	}
	if strings.Count(f.m.sidebar(), "SSO source: login") != 1 {
		t.Fatal("source not grouped")
	}
}

func TestCancelBusyOperationWaitsForCleanup(t *testing.T) {
	f := setupUI(t)
	cmd := f.m.operation("Activate roles", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
		return domain.Run{}, domain.ImportPlan{}, ctx.Err()
	})
	key(f.m, "esc")
	if !f.m.busy || !strings.Contains(f.m.status, "Waiting for cleanup") {
		t.Fatal("cancellation prematurely cleared transaction")
	}
	finish(f.m, cmd)
	if f.m.busy || !strings.Contains(f.m.status, "canceled") {
		t.Fatal("cancel result not shown")
	}
}

func TestCtrlCWhileBusyCancelsThenReviewsExit(t *testing.T) {
	f := setupUI(t)
	cmd := f.m.operation("Activate roles", func(ctx context.Context) (domain.Run, domain.ImportPlan, error) {
		return domain.Run{}, domain.ImportPlan{}, ctx.Err()
	})
	key(f.m, "ctrl+c")
	if f.m.review != nil || !f.m.busy {
		t.Fatal("exited before activation cleanup")
	}
	finish(f.m, f.m.load())
	if f.m.review != nil || !f.m.busy || f.sessions.shutdowns != 0 {
		t.Fatal("background refresh exited before operation cleanup")
	}
	finish(f.m, cmd)
	if f.m.review == nil || f.m.review.kind != "exit" || f.m.busy {
		t.Fatal("exit review missing after cancellation")
	}
}

func TestPerAliasStatusAndPersistenceFailure(t *testing.T) {
	f := setupUI(t)
	p, _ := f.m.profile()
	expiry := time.Now().Add(time.Hour)
	f.creds.roles = []domain.ManagedRole{{Alias: "missing-alias", Profile: p, Expiration: expiry, Status: "missing"}, {Alias: "active-alias", Profile: p, Expiration: expiry, Status: "active"}, {Alias: "unknown-alias", Profile: p, Expiration: expiry, Status: "unknown"}, {Alias: "conflict-alias", Profile: p, Expiration: expiry, Status: "conflict"}}
	finish(f.m, f.m.load())
	key(f.m, "5")
	f.m.height = 50
	v := f.m.View()

	for _, pair := range [][2]string{{"missing-alias", "missing"}, {"active-alias", "active"}, {"unknown-alias", "unknown"}, {"conflict-alias", "conflict"}} {
		found := false
		for _, line := range strings.Split(v, "\n") {
			if strings.Contains(line, pair[0]) && strings.Contains(line, pair[1]) && strings.Contains(line, "111111111111") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing credential row %v: %s", pair, v)
		}
	}

	if !strings.HasPrefix(f.m.roleStatus(p), "active until") {
		t.Fatal("valid alias not preferred")
	}
	f.m.services.SaveAccountAlias = func(string, string) error { return errors.New("settings write failed") }
	key(f.m, "tab") // y on the focused Credentials table copies the profile name; labels are edited from the sidebar
	key(f.m, "y")
	fill(f.m, "Uncommitted label")
	if f.m.aliases[p.AccountID] != "First account" || f.m.form == nil || !strings.Contains(f.m.status, "settings write failed") {
		t.Fatal("failed alias save changed label or lost editable form")
	}
}

func TestResizeHeightsScrollingAndModalHelp(t *testing.T) {
	f := setupUI(t)
	key(f.m, "2")
	key(f.m, "n")
	fill(f.m, "db", "db.example", "eu-west-1", "5432", "i-0123456789abcdef0", "us-east-1", "15432", "tunnel", "", "", "")
	for _, width := range []int{80, 100, 140} {
		f.m.width = width
		f.m.height = 24
		f.m.scroll = 0
		v := f.m.View()
		if len(strings.Split(v, "\n")) > 24 {
			t.Fatalf("height overflow at %d: %d\n%s", width, len(strings.Split(v, "\n")), v)
		}
		key(f.m, "pgdown")
		if !strings.Contains(f.m.View(), "localhost:15432") {
			t.Fatalf("route unreachable by scrolling: %s", f.m.View())
		}
		key(f.m, "pgup")
	}
	key(f.m, "?")
	if !strings.Contains(f.m.View(), "Keyboard help") {
		t.Fatal("modal help missing")
	}
	key(f.m, "esc")
	if f.m.review == nil {
		t.Fatal("help escape discarded review")
	}
	f.m.width = 30
	key(f.m, "ctrl+c")
	if !strings.Contains(f.m.View(), "Exit workspace?") {
		t.Fatal("narrow exit review hidden")
	}
}

func TestColorThemeAndSourceGrouping(t *testing.T) {
	f := setupUI(t)
	f.m.noColor = false
	for _, dark := range []bool{true, false} {
		f.m.dark = dark
		v := f.m.View()
		if !strings.Contains(v, "111111111111") || !strings.Contains(v, "ReadOnlyAccess") {
			t.Fatal("color theme hid identity")
		}
	}
	profiles := []domain.Profile{{Name: "z", SSOSession: "second", AccountID: "111111111111"}, {Name: "b", SSOSession: "first", AccountID: "222222222222"}, {Name: "a", SSOSession: "first", AccountID: "111111111111"}}
	m := New(domain.Services{Profiles: profiles}).(*Model)
	if m.services.Profiles[0].Name != "a" || m.services.Profiles[1].Name != "b" || profiles[0].Name != "z" || strings.Count(m.sidebar(), "SSO source: first") != 1 {
		t.Fatal("sorting mutated caller or failed grouping")
	}
}
