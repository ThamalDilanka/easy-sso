//go:build !windows

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/credentials"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/sessions"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
)

const testConfig = `[sso-session root]
sso_start_url=https://example.awsapps.com/start
sso_region=us-east-1
[profile one]
sso_session=root
sso_account_id=111111111111
sso_role_name=ReadOnlyAccess
region=us-east-1
[profile two]
source_profile=one
role_arn=arn:aws:iam::222222222222:role/OrganizationAccountAccessRole
region=us-east-1
`

type harness struct {
	dir, state, config, creds string
	cli                       *awscli.Client
	roles                     *credentials.Manager
	bookmarks                 *connections.Manager
	profiles                  []domain.Profile
}

func setup(t *testing.T) *harness {
	t.Helper()
	if _, e := exec.LookPath("python3"); e != nil {
		t.Fatal("Python 3 is required for the isolated AWS subprocess test double")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	h := &harness{dir: dir, state: filepath.Join(dir, "state"), config: filepath.Join(dir, "config"), creds: filepath.Join(dir, "credentials")}
	// Tests never inspect the current user's AWS files. Deliberately poison the
	// ambient provider to verify production CLI environment construction.
	t.Setenv("HOME", dir)
	t.Setenv("AWS_PROFILE", "ambient-admin")
	t.Setenv("AWS_ACCESS_KEY_ID", "AMBIENT_ADMIN_KEY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "AMBIENT_ADMIN_SECRET")
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::000000000000:role/Admin")
	t.Setenv("AWS_ENDPOINT_URL", "https://must-not-be-used.invalid")
	b, e := os.ReadFile("../../testdata/fake-aws.py")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "aws")
	if e = os.WriteFile(path, b, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(h.config, []byte(testConfig), 0600); e != nil {
		t.Fatal(e)
	}
	h.profiles, e = profiles.Load(h.config)
	if e != nil {
		t.Fatal(e)
	}
	h.cli, e = awscli.New(path, h.config, h.creds, h.state)
	if e != nil {
		t.Fatal(e)
	}
	h.roles, e = credentials.New(h.cli, h.config, h.creds, h.state)
	if e != nil {
		t.Fatal(e)
	}
	h.bookmarks, e = connections.New(h.state)
	if e != nil {
		t.Fatal(e)
	}
	h.bookmarks.SetProfiles(h.profiles)
	return h
}
func (h *harness) mode(t *testing.T, v map[string]any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if e := os.WriteFile(filepath.Join(h.dir, "mode.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
}
func (h *harness) activate(t *testing.T) []domain.ManagedRole {
	t.Helper()
	var sel []domain.Selection
	for _, p := range h.profiles {
		sel = append(sel, domain.Selection{Profile: p, Alias: "tui-" + p.Name})
	}
	r, e := h.roles.Activate(context.Background(), sel, true)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func availablePort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func db(port int) domain.Connection {
	return domain.Connection{Kind: "db", Name: "db", AccountID: "111111111111", Partition: "aws", Region: "eu-west-1", Endpoint: "db.example.rds.amazonaws.com", DBPort: 5432, LocalPort: port, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1"}
}
func docdb(port int) domain.Connection {
	c := db(port)
	c.Name, c.Endpoint, c.Region, c.DBPort = "docdb", "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com", "us-east-1", 27017
	return c
}

// A DocumentDB tunnel is an ordinary remote-host forward: sessions never
// fetch a CA bundle for it. Whether the relay carries several data sources
// at once is checked live (L3); the fake never accepts connections.
func TestDocumentDBTunnelJourney(t *testing.T) {
	h := setup(t)
	h.activate(t)
	role, snap, e := h.roles.Snapshot("tui-one")
	if e != nil {
		t.Fatal(e)
	}
	m := newManager(t, h, nil)
	ctx := context.Background()
	run, e := m.Open(ctx, docdb(availablePort(t)), role, snap)
	if e != nil {
		t.Fatal(e)
	}
	if run.State != "Listening" || run.SessionID == "" {
		t.Fatal(run)
	}
	calls, _ := os.ReadFile(filepath.Join(h.dir, "calls.jsonl"))
	for _, want := range []string{"AWS-StartPortForwardingSessionToRemoteHost", `\"host\":[\"example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com\"]`, `\"portNumber\":[\"27017\"]`, fmt.Sprintf(`\"localPortNumber\":[\"%d\"]`, run.Connection.LocalPort)} {
		if !bytes.Contains(calls, []byte(want)) {
			t.Fatalf("start call lacks %s", want)
		}
	}
	if _, e = os.Stat(filepath.Join(h.state, "rds-ca-aws.pem")); !os.IsNotExist(e) {
		t.Fatal("sessions fetched a CA bundle for a DocumentDB tunnel", e)
	}
	if e = m.Stop(ctx, run.ID); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(run.Connection.LocalPort)))
	if e != nil {
		t.Fatal("tunnel still listening after stop", e)
	}
	listener.Close()
}

func newManager(t *testing.T, h *harness, adapter terminal.Adapter) *sessions.Manager {
	t.Helper()
	m, e := sessions.NewWithOptions(h.cli, h.state, "unused-test-executable", h.bookmarks, sessions.Options{Terminal: adapter, Attempts: 80, Interval: 25 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = m.Shutdown(ctx)
	})
	return m
}
func TestCompleteActivationTunnelBackupAndRemovalJourney(t *testing.T) {
	h := setup(t)
	h.activate(t)
	role, snap, e := h.roles.Snapshot("tui-one")
	if e != nil {
		t.Fatal(e)
	}
	m := newManager(t, h, nil)
	ctx := context.Background()
	first, e := m.Open(ctx, db(availablePort(t)), role, snap)
	if e != nil {
		t.Fatal(e)
	}
	if first.State != "Listening" || first.SessionID == "" {
		t.Fatal(first)
	}
	secondC := db(availablePort(t))
	secondC.Name = "other-db"
	secondC.Endpoint = "other.example.rds.amazonaws.com"
	second, e := m.Open(ctx, secondC, role, snap)
	if e != nil {
		t.Fatal(e)
	}
	socket, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(first.Connection.LocalPort)), time.Second)
	if e != nil {
		t.Fatal(e)
	}
	socket.Close()
	// Removing local files neither retargets nor revokes established runs.
	if e = h.roles.Remove("tui-one"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = h.roles.Snapshot("tui-one"); e == nil {
		t.Fatal("removed alias launched")
	}
	if e = m.Stop(ctx, first.ID); e != nil {
		t.Fatal(e)
	}
	runs := m.List()
	for _, r := range runs {
		if r.ID == second.ID && r.State != "Listening" {
			t.Fatal("unrelated tunnel affected", r)
		}
	}
	export := filepath.Join(h.dir, "backup.json")
	if e = h.bookmarks.Export(export); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(export)
	for _, secret := range []string{"SECRET_TEST_ONLY", "TOKEN_TEST_ONLY", "ASIA_TEST_"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("secret in backup")
		}
	}
	restored, e := connections.New(filepath.Join(h.dir, "restored"))
	if e != nil {
		t.Fatal(e)
	}
	plan, e := restored.Preview(export)
	if e != nil || len(plan.Additions) != 2 {
		t.Fatal(plan, e)
	}
	if e = restored.Commit(plan, false); e != nil {
		t.Fatal(e)
	}
	cs, _ := restored.List()
	if cs[0].RelayRegion != "us-east-1" || cs[0].Region != "eu-west-1" || cs[0].LastUsedAt == nil {
		t.Fatal("resource identity lost", cs)
	}
	if e = m.Shutdown(ctx); e != nil {
		t.Fatal(e)
	}
	for _, r := range m.List() {
		if r.Connection.Kind == "db" && r.State != "Stopped" {
			t.Fatal("owned tunnel survived normal exit", r)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(h.dir, "calls.jsonl"))
	if bytes.Contains(calls, []byte(`"ambientProvider": true`)) {
		t.Fatal("ambient credential override leaked")
	}
	if !bytes.Contains(calls, []byte("AWS-StartPortForwardingSessionToRemoteHost")) {
		t.Fatal("wrong SSM document")
	}
}

type helperTerminal struct {
	state  string
	mu     sync.Mutex
	errors []error
}

func (a *helperTerminal) Capabilities() terminal.Capabilities {
	return terminal.Capabilities{LaunchTab: true}
}
func (a *helperTerminal) Launch(_ context.Context, r terminal.Request) (string, error) {
	go func() {
		e := sessions.RunHelper(context.Background(), a.state, r.RunID)
		a.mu.Lock()
		a.errors = append(a.errors, e)
		a.mu.Unlock()
	}()
	return r.RunID, nil
}
func (a *helperTerminal) Focus(string) error { return os.ErrPermission }
func TestTwoIndependentShellsExactTerminationAndRestartRecovery(t *testing.T) {
	h := setup(t)
	h.activate(t)
	role, snap, e := h.roles.Snapshot("tui-one")
	if e != nil {
		t.Fatal(e)
	}
	adapter := &helperTerminal{state: h.state}
	m := newManager(t, h, adapter)
	c := domain.Connection{Kind: "ec2", Name: "api", AccountID: role.Profile.AccountID, Partition: "aws", Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}
	a, e := m.Open(context.Background(), c, role, snap)
	if e != nil {
		t.Fatal(e)
	}
	b, e := m.Open(context.Background(), c, role, snap)
	if e != nil {
		t.Fatal(e)
	}
	defer m.Stop(context.Background(), a.ID)
	defer m.Stop(context.Background(), b.ID)
	if a.ID == b.ID || a.SessionID == b.SessionID || a.State != "Connected" || b.State != "Connected" {
		t.Fatal(a, b)
	}
	if e = m.Focus(a.ID); e == nil {
		t.Fatal("unsupported focus succeeded")
	}
	if e = m.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	restarted := newManager(t, h, adapter)
	if len(restarted.List()) != 2 {
		t.Fatal("helper recovery lost sessions")
	}
	if e = restarted.Stop(context.Background(), a.ID); e != nil {
		t.Fatal(e)
	}
	if e = restarted.Recheck(context.Background(), b.ID); e != nil {
		t.Fatal(e)
	}
	for _, r := range restarted.List() {
		if r.ID == b.ID && r.State != "Connected" {
			t.Fatal("targeted disconnect ended another shell")
		}
	}
	for _, name := range []string{"credential-state.json", "connections.json"} {
		data, _ := os.ReadFile(filepath.Join(h.state, name))
		if bytes.Contains(data, []byte("TOKEN_TEST_ONLY")) {
			t.Fatal("secret persisted in metadata")
		}
	}
	files, _ := os.ReadDir(filepath.Join(h.state, "runtime"))
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(h.state, "runtime", f.Name()))
			if bytes.Contains(data, []byte("TOKEN_TEST_ONLY")) || bytes.Contains(data, []byte("SECRET_TEST_ONLY")) {
				t.Fatal("secret persisted in helper registration")
			}
		}
	}
}
func TestCleanupFailureRecoveryIdentityBlockAndUnknownNoSave(t *testing.T) {
	h := setup(t)
	h.mode(t, map[string]any{"logoutFailure": true})
	p := h.profiles[0]
	if _, e := h.roles.Activate(context.Background(), []domain.Selection{{Profile: p, Alias: "tui-one"}}, true); e == nil {
		t.Fatal("cleanup failure reported success")
	}
	if _, e := os.Stat(h.creds); !os.IsNotExist(e) {
		t.Fatal("cleanup failure published credentials")
	}
	h.mode(t, map[string]any{})
	if e := h.roles.RetryCleanup(context.Background()); e != nil {
		t.Fatal(e)
	}
	h.activate(t)
	role, snap, e := h.roles.Snapshot("tui-one")
	if e != nil {
		t.Fatal(e)
	}
	m := newManager(t, h, nil)
	h.mode(t, map[string]any{"identityMismatch": true})
	if _, e = m.Open(context.Background(), db(availablePort(t)), role, snap); e == nil {
		t.Fatal("STS mismatch launched")
	}
	h.mode(t, map[string]any{"unconfirmed": true})
	r, e := m.Open(context.Background(), db(availablePort(t)), role, snap)
	if e == nil || r.State != "Unknown" || r.SessionID != "" {
		t.Fatal("unconfirmed session was mislabelled", r, e)
	}
	cs, _ := h.bookmarks.List()
	if len(cs) != 0 {
		t.Fatal("unconfirmed connection saved")
	}
	_ = m.Stop(context.Background(), r.ID)
}
func TestPortConflictAndUnsafePublicListener(t *testing.T) {
	h := setup(t)
	h.activate(t)
	role, snap, e := h.roles.Snapshot("tui-one")
	if e != nil {
		t.Fatal(e)
	}
	m := newManager(t, h, nil)
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if _, e = m.Open(context.Background(), db(l.Addr().(*net.TCPAddr).Port), role, snap); e == nil {
		t.Fatal("occupied port accepted")
	}
	h.mode(t, map[string]any{"publicListener": true})
	r, e := m.Open(context.Background(), db(availablePort(t)), role, snap)
	if e == nil || r.State != "Failed" {
		t.Fatal("public listener accepted", r, e)
	}
	cs, _ := h.bookmarks.List()
	if len(cs) != 0 {
		t.Fatal("unsafe tunnel saved")
	}
}

func TestRecoveredTunnelVerificationAndExactRemoteStop(t *testing.T) {
	h := setup(t)
	h.activate(t)
	role, snapshot, err := h.roles.Snapshot("tui-one")
	if err != nil {
		t.Fatal(err)
	}
	owner := newManager(t, h, nil)
	first, err := owner.Open(context.Background(), db(availablePort(t)), role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := owner.Open(context.Background(), db(availablePort(t)), role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := sessions.NewWithOptions(h.cli, h.state, "unused", h.bookmarks, sessions.Options{RecoveryCredentials: h.roles.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Shutdown(context.Background())
	recovered.Reconcile(context.Background())
	for _, run := range recovered.List() {
		if run.State != "Remote active" || recovered.OwnsTunnel(run.ID) {
			t.Fatal("incorrect recovered ownership or status", run)
		}
	}
	if err := recovered.Stop(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	for _, run := range recovered.List() {
		if run.ID == first.ID && (run.State != "Stopped" || !run.RemoteEnded) {
			t.Fatal("stop was not confirmed", run)
		}
		if run.ID == second.ID && run.State != "Remote active" {
			t.Fatal("unrelated tunnel changed", run)
		}
	}
	// The verified remote termination must let the original forwarding process exit.
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(first.Connection.LocalPort)))
	if err != nil {
		t.Fatal("selected tunnel still listening", err)
	}
	listener.Close()
	socket, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(second.Connection.LocalPort)), time.Second)
	if err != nil {
		t.Fatal("other tunnel was closed", err)
	}
	socket.Close()
}
