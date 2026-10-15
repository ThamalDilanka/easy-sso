package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/ipc"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeChild struct {
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	stops int
}

func newChild() *fakeChild       { return &fakeChild{done: make(chan struct{})} }
func (c *fakeChild) Wait() error { <-c.done; return nil }
func (c *fakeChild) Stop() error {
	c.mu.Lock()
	c.stops++
	c.mu.Unlock()
	c.once.Do(func() { close(c.done) })
	return nil
}

type fakeCLI struct {
	mu                                  sync.Mutex
	identity                            domain.Identity
	metadata                            []domain.SessionMetadata
	children                            map[string]*fakeChild
	starts                              [][]string
	snapshots                           []domain.Snapshot
	terminations                        []string
	describeErr, startErr, terminateErr error
	wrong                               string
	none                                bool
}

func (f *fakeCLI) HelperConfig() (string, string, string) {
	return "fake-aws", "fake-config", "fake-credentials"
}
func (f *fakeCLI) Identity(context.Context, domain.Snapshot, string) (domain.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity, nil
}
func (f *fakeCLI) Start(_ context.Context, s domain.Snapshot, a []string, _ io.Reader, _, _ io.Writer) (domain.Child, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.starts = append(f.starts, append([]string(nil), a...))
	f.snapshots = append(f.snapshots, s)
	reason, target := "", ""
	for i, v := range a {
		if v == "--reason" {
			reason = a[i+1]
		}
		if v == "--target" {
			target = a[i+1]
		}
	}
	c := newChild()
	id := "session-" + strings.TrimPrefix(reason, "easy-sso:")
	if f.children == nil {
		f.children = map[string]*fakeChild{}
	}
	f.children[id] = c
	m := domain.SessionMetadata{SessionID: id, Reason: reason, Owner: f.identity.ARN, Target: target, StartDate: time.Now().UTC(), Status: "Connected"}
	switch f.wrong {
	case "reason":
		m.Reason += "wrong"
	case "owner":
		m.Owner += "wrong"
	case "target":
		m.Target += "wrong"
	case "time":
		m.StartDate = time.Now().Add(-time.Hour)
	case "future":
		m.StartDate = time.Now().Add(time.Hour)
	case "status":
		m.Status = "Connecting"
	case "id":
		m.SessionID = ""
	}
	if !f.none {
		f.metadata = append(f.metadata, m)
		if f.wrong == "ambiguous" {
			m.SessionID += "duplicate"
			f.metadata = append(f.metadata, m)
		}
	}
	return c, nil
}
func (f *fakeCLI) Describe(_ context.Context, _ domain.Snapshot, _ string, a []string) ([]domain.SessionMetadata, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(a) != 1 || !strings.HasPrefix(a[0], "[") {
		return nil, errors.New("bad filter interface")
	}
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return append([]domain.SessionMetadata(nil), f.metadata...), nil
}
func (f *fakeCLI) Terminate(_ context.Context, _ domain.Snapshot, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminations = append(f.terminations, id)
	return f.terminateErr
}

type bookmarks struct {
	mu    sync.Mutex
	saved []domain.Connection
}

func (b *bookmarks) List() ([]domain.Connection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Connection(nil), b.saved...), nil
}
func (b *bookmarks) Save(c domain.Connection) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.saved = append(b.saved, c)
	return nil
}
func fixture(t *testing.T) (domain.Connection, domain.ManagedRole, domain.Snapshot, *fakeCLI) {
	exp := time.Now().Add(time.Hour).UTC()
	id := domain.Identity{Account: "123456789012", ARN: "arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_ReadOnly_0123456789abcdef/dev", UserID: "id:dev"}
	p := domain.Profile{Name: "readonly", Partition: "aws", AccountID: id.Account, Role: "ReadOnly", DirectSSO: true, Region: "us-east-1", Chain: []string{"readonly"}}
	role := domain.ManagedRole{Profile: p, Alias: "tui-readonly", Identity: id, Expiration: exp, Status: "Active"}
	s := domain.Snapshot{AccessKeyID: "ASIATEST", SecretAccessKey: "TOP-SECRET", SessionToken: "SESSION-TOKEN", Expiration: exp}
	c := domain.Connection{Kind: "db", Name: "app", Partition: "aws", AccountID: id.Account, Region: "us-east-1", Endpoint: "app.example.rds.amazonaws.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-west-2"}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.LocalPort = l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return c, role, s, &fakeCLI{identity: id}
}
func manager(t *testing.T, cli *fakeCLI, b *bookmarks, o Options) *Manager {
	t.Helper()
	if o.Attempts == 0 {
		o.Attempts = 3
	}
	if o.Interval == 0 {
		o.Interval = time.Millisecond
	}
	if o.Probe == nil {
		o.Probe = func(int, domain.Child) (bool, error) { return true, nil }
	}
	m, e := NewWithOptions(cli, t.TempDir(), "/fake/easysso", b, o)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m
}
func TestTunnelLifecycleFrozenIdentityRegionsAndTargetedStop(t *testing.T) {
	c, r, s, f := fixture(t)
	b := &bookmarks{}
	m := manager(t, f, b, Options{})
	a, e := m.Open(context.Background(), c, r, s)
	if e != nil || a.State != "Listening" {
		t.Fatalf("%+v %v", a, e)
	}
	c.LocalPort++
	other, e := m.Open(context.Background(), c, r, s)
	if e != nil {
		t.Fatal(e)
	}
	r.Profile.Chain[0] = "changed"
	s.AccessKeyID = "replaced"
	runs := m.List()
	runs[0].Profile.Chain[0] = "mutated"
	if m.List()[0].Profile.Chain[0] != "readonly" {
		t.Fatal("run chain was not immutable")
	}
	if e = m.Stop(context.Background(), a.ID); e != nil {
		t.Fatal(e)
	}
	if len(f.terminations) != 1 || f.terminations[0] != a.SessionID {
		t.Fatal("wrong session terminated")
	}
	if m.List()[1].State != "Listening" {
		t.Fatal("unrelated tunnel changed")
	}
	if f.snapshots[0].AccessKeyID != "ASIATEST" {
		t.Fatal("snapshot changed")
	}
	args := strings.Join(f.starts[0], " ")
	if !strings.Contains(args, "--region us-west-2") || !strings.Contains(args, "AWS-StartPortForwardingSessionToRemoteHost") {
		t.Fatal(args)
	}
	if e = m.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.terminations) != 2 || f.terminations[1] != other.SessionID {
		t.Fatal("shutdown did not stop exactly owned tunnels")
	}
	if len(b.saved) != 2 {
		t.Fatal("confirmed bookmarks not saved")
	}
}
func TestCorrelationRejectsGuessesAndNeverSaves(t *testing.T) {
	for _, bad := range []string{"reason", "owner", "target", "time", "future", "status", "id", "ambiguous"} {
		t.Run(bad, func(t *testing.T) {
			c, r, s, f := fixture(t)
			f.wrong = bad
			b := &bookmarks{}
			m := manager(t, f, b, Options{})
			run, e := m.Open(context.Background(), c, r, s)
			if e == nil || run.State != "Unknown" {
				t.Fatalf("%+v %v", run, e)
			}
			if len(b.saved) != 0 {
				t.Fatal("unconfirmed session saved")
			}
			if bad != "status" && run.SessionID != "" {
				t.Fatal("guessed session ID")
			}
			_ = m.Stop(context.Background(), run.ID)
			if bad != "status" && len(f.terminations) != 0 {
				t.Fatal("terminated guessed session")
			}
		})
	}
}
func TestPrelaunchRejectsIdentityAndExpiration(t *testing.T) {
	for _, kind := range []string{"account", "arn", "userid", "expired", "snapshot-expiry", "partition-region", "relay-region", "missing"} {
		t.Run(kind, func(t *testing.T) {
			c, r, s, f := fixture(t)
			switch kind {
			case "account":
				f.identity.Account = "000000000000"
			case "arn":
				f.identity.ARN = strings.Replace(f.identity.ARN, "ReadOnly", "Admin", 1)
			case "userid":
				f.identity.UserID = "changed"
			case "expired":
				s.Expiration = time.Now().Add(-time.Hour)
				r.Expiration = s.Expiration
			case "snapshot-expiry":
				s.Expiration = s.Expiration.Add(time.Second)
			case "partition-region":
				c.Region = "cn-north-1"
			case "relay-region":
				c.RelayRegion = "cn-north-1"
			case "missing":
				s.SessionToken = ""
			}
			m := manager(t, f, &bookmarks{}, Options{})
			if _, e := m.Open(context.Background(), c, r, s); e == nil {
				t.Fatal("invalid context accepted")
			}
			if len(f.starts) != 0 {
				t.Fatal("launched before validation")
			}
		})
	}
}
func TestPortConflictAndRaceAndWildcard(t *testing.T) {
	for _, kind := range []string{"occupied", "race", "wildcard", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			c, r, s, f := fixture(t)
			b := &bookmarks{}
			o := Options{}
			if kind == "occupied" {
				l, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
				c.LocalPort = l.Addr().(*net.TCPAddr).Port
			} else if kind == "timeout" {
				o.Probe = func(int, domain.Child) (bool, error) { return false, nil }
			} else {
				o.Probe = func(int, domain.Child) (bool, error) { return false, errors.New(kind) }
			}
			m := manager(t, f, b, o)
			run, e := m.Open(context.Background(), c, r, s)
			if e == nil {
				t.Fatal("unsafe port accepted")
			}
			if len(b.saved) != 0 {
				t.Fatal("saved unsafe/unready tunnel")
			}
			if kind != "occupied" {
				if run.State != "Failed" {
					t.Fatal(run.State)
				}
				if len(f.terminations) != 1 {
					t.Fatal("known session not terminated")
				}
				if f.children[run.SessionID].stops == 0 {
					t.Fatal("owned child not cleaned up")
				}
			} else if len(f.starts) != 0 {
				t.Fatal("started occupied port")
			}
		})
	}
}
func TestMetadataFailureRecoveryAndRemoteTerminationFailure(t *testing.T) {
	c, r, s, f := fixture(t)
	f.describeErr = errors.New("denied")
	b := &bookmarks{}
	m := manager(t, f, b, Options{})
	run, e := m.Open(context.Background(), c, r, s)
	if e == nil || run.State != "Unknown" {
		t.Fatal(run, e)
	}
	f.mu.Lock()
	f.describeErr = nil
	f.mu.Unlock()
	if e = m.Recheck(context.Background(), run.ID); e != nil {
		t.Fatal(e)
	}
	if m.List()[0].State != "Listening" || len(b.saved) != 1 {
		t.Fatal("recovery did not confirm tunnel")
	}
	f.terminateErr = errors.New("denied")
	if e = m.Stop(context.Background(), run.ID); e == nil {
		t.Fatal("remote termination failure hidden")
	}
	result := m.List()[0]
	if result.RemoteEnded || result.State != "Stopped" || !strings.Contains(result.Error, "unconfirmed") {
		t.Fatal(result)
	}
}

// Helpers perform asynchronous IPC and durable file writes. Tests using them
// allow five seconds for readiness, while polling promptly when it is available.
type helperTerminal struct {
	startupDelay time.Duration
	wg           sync.WaitGroup
	cli          domain.SessionCLI
	mu           sync.Mutex
	launched     int
	err          error
	cancel       []context.CancelFunc
}

func (t *helperTerminal) Capabilities() terminal.Capabilities {
	return terminal.Capabilities{LaunchWindow: true}
}
func (t *helperTerminal) Focus(string) error { return errors.New("unsupported") }
func (t *helperTerminal) Launch(_ context.Context, r terminal.Request) (string, error) {
	t.mu.Lock()
	t.launched++
	e := t.err
	t.mu.Unlock()
	if e != nil {
		return "", e
	}
	var ep ipc.Endpoint
	if e = ipc.ReadJSON(filepath.Join(r.StateDir, "runtime", r.RunID+"-startup.json"), &ep); e != nil {
		return "", e
	}
	var data startup
	if e = ipc.Call(ep, "start", &data); e != nil {
		return "", e
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.cancel = append(t.cancel, cancel)
	t.mu.Unlock()
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		if t.startupDelay > 0 {
			if err := pause(ctx, t.startupDelay); err != nil {
				return
			}
		}
		_ = runHelperWithCLI(ctx, r.StateDir, data, t.cli)
	}()
	return r.RunID, nil
}
func (t *helperTerminal) close() {
	t.mu.Lock()
	for _, c := range t.cancel {
		c()
	}
	t.mu.Unlock()
	t.wg.Wait()
}
func TestIndependentShellsReconnectFocusAndShutdown(t *testing.T) {
	c, r, s, f := fixture(t)
	c.Kind = "ec2"
	c.InstanceID = c.RelayInstanceID
	c.Endpoint = ""
	c.DBPort = 0
	c.LocalPort = 0
	c.RelayInstanceID = ""
	c.RelayRegion = ""
	term := &helperTerminal{cli: f}
	defer term.close()
	b := &bookmarks{}
	dir := t.TempDir()
	m, e := NewWithOptions(f, dir, "/fake/easysso", b, Options{Terminal: term, Attempts: 1000, Interval: 5 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Shutdown(context.Background())
	a, e := m.Open(context.Background(), c, r, s)
	if e != nil || a.State != "Connected" {
		t.Fatal(a, e)
	}
	other, e := m.Open(context.Background(), c, r, s)
	if e != nil || other.State != "Connected" || a.SessionID == other.SessionID {
		t.Fatal(other, e)
	}
	if e = m.Focus(a.ID); e == nil {
		t.Fatal("advertised unqualified focus")
	}
	if term.launched != 2 {
		t.Fatal("focus launched duplicate")
	}
	if e = m.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.terminations) != 0 {
		t.Fatal("normal workspace quit killed independent shells")
	}
	restarted, e := NewWithOptions(f, dir, "/fake/easysso", b, Options{Terminal: term})
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Shutdown(context.Background())
	if len(restarted.List()) != 2 {
		t.Fatal("helpers not rediscovered")
	}
	if e = restarted.Recheck(context.Background(), a.ID); e != nil {
		t.Fatal(e)
	}
	if e = restarted.Stop(context.Background(), a.ID); e != nil {
		t.Fatal(e)
	}
	if len(f.terminations) != 1 || f.terminations[0] != a.SessionID {
		t.Fatal("wrong recovered shell terminated")
	}
	for _, rr := range restarted.List() {
		if rr.ID == other.ID && rr.State != "Connected" {
			t.Fatal("other shell was affected")
		}
	}
	files, _ := os.ReadDir(filepath.Join(dir, "runtime"))
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".json") {
			bytes, _ := os.ReadFile(filepath.Join(dir, "runtime", file.Name()))
			for _, secret := range []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken} {
				if strings.Contains(string(bytes), secret) {
					t.Fatal("secret persisted")
				}
			}
		}
	}
}
func TestTerminalLaunchFailureDoesNotSave(t *testing.T) {
	c, r, s, f := fixture(t)
	c.Kind = "ec2"
	c.InstanceID = c.RelayInstanceID
	c.Endpoint = ""
	c.DBPort = 0
	c.LocalPort = 0
	c.RelayInstanceID = ""
	c.RelayRegion = ""
	b := &bookmarks{}
	m := manager(t, f, b, Options{Terminal: &helperTerminal{err: errors.New("launch failed")}})
	run, e := m.Open(context.Background(), c, r, s)
	if e == nil || run.State != "Failed" || len(b.saved) != 0 || len(f.starts) != 0 {
		t.Fatal(run, e)
	}
}
func TestStaleRegistrationAndPIDReuseNeverKilled(t *testing.T) {
	c, r, s, f := fixture(t)
	b := &bookmarks{}
	dir := t.TempDir()
	id := runID()
	reg := registration{Run: domain.Run{ID: id, Connection: c, Profile: r.Profile, Identity: r.Identity, SessionID: "known-old-session", State: "Listening", Expiration: s.Expiration}, PID: os.Getpid(), Heartbeat: time.Now().Add(-time.Hour)}
	if e := ipc.WriteJSON(filepath.Join(dir, "runtime", id+".json"), reg); e != nil {
		t.Fatal(e)
	}
	m, e := NewWithOptions(f, dir, "/fake", b, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Shutdown(context.Background())
	if m.OwnsTunnel(id) {
		t.Fatal("recovered PID was treated as an owned tunnel")
	}
	if m.List()[0].State != "Unknown" {
		t.Fatal("stale metadata rendered live")
	}
	if e = m.Stop(context.Background(), id); e == nil {
		t.Fatal("claimed recovered PID was safely stopped")
	}
	if len(f.terminations) != 0 {
		t.Fatal("terminated with missing recovered credentials")
	}
	var persisted registration
	if err := ipc.ReadJSON(filepath.Join(dir, "runtime", id+".json"), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Run.State != reg.Run.State || persisted.Run.Error != reg.Run.Error {
		t.Fatal("unowned stop overwrote another instance's registration")
	}
}
func TestJSONParameterStructuredSingleArgument(t *testing.T) {
	c, _, _, _ := fixture(t)
	args := arguments(domain.Run{ID: runID(), Connection: c})
	for i, a := range args {
		if a == "--parameters" {
			var p map[string][]string
			if e := json.Unmarshal([]byte(args[i+1]), &p); e != nil {
				t.Fatal(e)
			}
			if p["host"][0] != c.Endpoint || p["localPortNumber"][0] != fmt.Sprint(c.LocalPort) {
				t.Fatal(p)
			}
			return
		}
	}
	t.Fatal("parameters missing")
}
func TestCancellationDuringListenerReadinessCleansTunnel(t *testing.T) {
	c, r, s, f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := manager(t, f, &bookmarks{}, Options{Probe: func(int, domain.Child) (bool, error) { cancel(); return false, nil }})
	run, e := m.Open(ctx, c, r, s)
	if e == nil || run.State != "Stopped" || !run.RemoteEnded || len(f.terminations) != 1 {
		t.Fatal(run, e)
	}
	if f.children[run.SessionID].stops == 0 {
		t.Fatal("canceled tunnel child left running")
	}
}
func TestHelperResponseCannotChangeFrozenBinding(t *testing.T) {
	c, r, s, f := fixture(t)
	c.Kind = "ec2"
	c.InstanceID = c.RelayInstanceID
	c.Endpoint = ""
	c.DBPort = 0
	c.LocalPort = 0
	c.RelayInstanceID = ""
	c.RelayRegion = ""
	b := &bookmarks{}
	m := manager(t, f, b, Options{})
	run := domain.Run{ID: runID(), Connection: c, Profile: r.Profile, Alias: r.Alias, Identity: r.Identity, Expiration: s.Expiration, StartedAt: time.Now().UTC(), State: "Connected"}
	server, e := ipc.Listen(filepath.Join(m.stateDir, "runtime"), "test", func(string) (any, error) {
		forged := copyRun(run)
		forged.Connection.AccountID = "000000000000"
		return forged, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close()
	m.runs[run.ID] = &entry{run: run, endpoint: server.Endpoint}
	if e = m.Recheck(context.Background(), run.ID); e == nil {
		t.Fatal("helper changed frozen account")
	}
	result := m.List()[0]
	if result.Connection.AccountID != c.AccountID || result.State != "Unknown" || len(b.saved) != 0 {
		t.Fatal(result)
	}
}
func TestShellTitlesUseAliasAndStayUniqueAfterClosingMiddle(t *testing.T) {
	c, r, s, f := fixture(t)
	c.Kind = "ec2"
	c.InstanceID = c.RelayInstanceID
	c.Endpoint = ""
	c.DBPort = 0
	c.LocalPort = 0
	c.RelayInstanceID = ""
	c.RelayRegion = ""
	// Exercise delayed helper registration instead of relying on fast scheduling.
	term := &helperTerminal{cli: f, startupDelay: 250 * time.Millisecond}
	defer term.close()
	dir := filepath.Join(t.TempDir(), "state")
	m, e := NewWithOptions(f, dir, "/fake/easysso", &bookmarks{}, Options{Terminal: term, Attempts: 1000, Interval: 5 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Shutdown(context.Background())
	if e = store.SaveAlias(dir, c.AccountID, "Sandbox"); e != nil {
		t.Fatal(e)
	}
	var runs []domain.Run
	for i := 0; i < 3; i++ {
		run, e := m.Open(context.Background(), c, r, s)
		if e != nil {
			t.Fatal(e)
		}
		runs = append(runs, run)
	}
	if runs[0].Title != "app · Sandbox" || runs[1].Title != "app · Sandbox #2" || runs[2].Title != "app · Sandbox #3" {
		t.Fatal(runs)
	}
	if e = m.Stop(context.Background(), runs[1].ID); e != nil {
		t.Fatal(e)
	}
	next, e := m.Open(context.Background(), c, r, s)
	if e != nil {
		t.Fatal(e)
	}
	if next.Title != "app · Sandbox #4" {
		t.Fatal("duplicate title after closing middle", next.Title)
	}
}

func TestRecoverEC2Lifecycle(t *testing.T) {
	for _, state := range []string{"Ended", "Failed", "Connected"} {
		t.Run(state, func(t *testing.T) {
			c, role, snapshot, _ := fixture(t)
			c.Kind, c.InstanceID = "ec2", c.RelayInstanceID
			c.Endpoint, c.RelayInstanceID, c.RelayRegion = "", "", ""
			c.DBPort, c.LocalPort = 0, 0
			id, dir := runID(), t.TempDir()
			reg := registration{Run: domain.Run{ID: id, Connection: c, Profile: role.Profile, Identity: role.Identity, Expiration: snapshot.Expiration, State: state, Error: "original diagnostic"}, Endpoint: ipc.Endpoint{Address: "unavailable"}, Heartbeat: time.Now().Add(-time.Hour), PID: os.Getpid()}
			if err := ipc.WriteJSON(filepath.Join(dir, "runtime", id+".json"), reg); err != nil {
				t.Fatal(err)
			}
			m := &Manager{stateDir: dir, runs: map[string]*entry{}}
			m.recover()
			runs := m.List()
			want := state
			if state == "Connected" {
				want = "Unknown"
			}
			if len(runs) != 1 || runs[0].State != want {
				t.Fatalf("recovered %s: got %+v, want %s", state, runs, want)
			}
			if state != "Connected" && runs[0].Error != reg.Run.Error {
				t.Fatal("lost terminal diagnostic")
			}
			if m.runs[id].child != nil || m.runs[id].snapshot.AccessKeyID != "" {
				t.Fatal("recovery claimed process or credentials")
			}
		})
	}
}

type deniedCLI struct{ *fakeCLI }
type exitedChild struct{}

func (exitedChild) Wait() error { return errors.New("exit status 254") }
func (exitedChild) Stop() error { return nil }
func (f deniedCLI) Start(_ context.Context, _ domain.Snapshot, _ []string, _ io.Reader, _, stderr io.Writer) (domain.Child, error) {
	_, _ = io.WriteString(stderr, "aws: [ERROR]: An error occurred (AccessDeniedException) when calling the StartSession operation: not authorized for ssm:StartSession on SSM-SessionManagerRunShell\n")
	return exitedChild{}, nil
}
func TestShellPermissionDeniedReportsActionableFailure(t *testing.T) {
	c, role, snapshot, cli := fixture(t)
	c.Kind, c.InstanceID = "ec2", c.RelayInstanceID
	c.Endpoint, c.RelayInstanceID, c.RelayRegion = "", "", ""
	c.DBPort, c.LocalPort = 0, 0
	cli.none = true
	term := &helperTerminal{cli: deniedCLI{cli}}
	defer term.close()
	m := manager(t, cli, &bookmarks{}, Options{Terminal: term, Attempts: 1000, Interval: 5 * time.Millisecond})
	run, err := m.Open(context.Background(), c, role, snapshot)
	if err == nil || !strings.Contains(err.Error(), "Access denied") || !strings.Contains(err.Error(), "ssm:StartSession") || run.State != "Failed" {
		t.Fatalf("denied launch: state=%s error=%v; want Failed and actionable access denial", run.State, err)
	}
}

type ownedHelperTerminal struct {
	helperTerminal
	owned    map[string]bool
	closeErr error
	exited   bool
}

func (t *ownedHelperTerminal) Launch(ctx context.Context, r terminal.Request) (string, error) {
	id, err := t.helperTerminal.Launch(ctx, r)
	if err == nil {
		t.mu.Lock()
		if t.owned == nil {
			t.owned = map[string]bool{}
		}
		t.owned[id] = true
		t.mu.Unlock()
	}
	return id, err
}
func (t *ownedHelperTerminal) Has(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.owned[id]
}
func (t *ownedHelperTerminal) Exited(string) bool { return t.exited }
func (t *ownedHelperTerminal) Close(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closeErr != nil {
		return t.closeErr
	}
	delete(t.owned, id)
	return nil
}
func TestCloseShellStopsOnlyOwnedHelperAndReportsRemoteFailure(t *testing.T) {
	for _, remoteFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(remoteFailure), func(t *testing.T) {
			c, role, snapshot, cli := fixture(t)
			c.Kind, c.InstanceID = "ec2", c.RelayInstanceID
			c.Endpoint, c.RelayInstanceID, c.RelayRegion = "", "", ""
			c.DBPort, c.LocalPort = 0, 0
			if remoteFailure {
				cli.terminateErr = errors.New("remote denied")
			}
			term := &ownedHelperTerminal{helperTerminal: helperTerminal{cli: cli}}
			defer term.close()
			m := manager(t, cli, &bookmarks{}, Options{Terminal: term, Attempts: 1000, Interval: 5 * time.Millisecond})
			run, err := m.Open(context.Background(), c, role, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.CloseShell(context.Background(), "not-owned"); err == nil {
				t.Fatal("accepted an unowned shell")
			}
			err = m.CloseShell(context.Background(), run.ID)
			if (err != nil) != remoteFailure {
				t.Fatalf("remote failure %v: %v", remoteFailure, err)
			}
			if term.Has(run.ID) {
				t.Fatal("local screen remains after close")
			}
			term.wg.Wait()
			cli.mu.Lock()
			defer cli.mu.Unlock()
			if len(cli.terminations) != 1 || cli.terminations[0] != run.SessionID {
				t.Fatal("close targeted wrong session")
			}
			child := cli.children[run.SessionID]
			child.mu.Lock()
			defer child.mu.Unlock()
			if child.stops == 0 {
				t.Fatal("close left local SSM client running")
			}
		})
	}
}

func TestCloseExitedOrFailedShellDoesNotNeedLiveHelper(t *testing.T) {
	for _, state := range []string{"Unknown", "Failed"} {
		t.Run(state, func(t *testing.T) {
			_, _, _, cli := fixture(t)
			id := runID()
			term := &ownedHelperTerminal{owned: map[string]bool{id: true}, exited: true}
			en := &entry{run: domain.Run{ID: id, State: state, Connection: domain.Connection{Kind: "ec2"}}, endpoint: ipc.Endpoint{Address: "unavailable"}}
			m := &Manager{cli: cli, options: Options{Terminal: term}, runs: map[string]*entry{id: en}}
			err := m.CloseShell(context.Background(), id)
			if state == "Unknown" && (err == nil || !strings.Contains(err.Error(), "unconfirmed")) {
				t.Fatal("lost remote status uncertainty")
			}
			if state == "Failed" && err != nil {
				t.Fatal(err)
			}
			if term.Has(id) || len(cli.terminations) != 0 {
				t.Fatal("dead screen retained or unconfirmed remote session targeted")
			}
		})
	}
}

func TestCloseShellKeepsScreenOnLocalCleanupFailure(t *testing.T) {
	id := runID()
	term := &ownedHelperTerminal{owned: map[string]bool{id: true}, closeErr: errors.New("close failed")}
	en := &entry{run: domain.Run{ID: id, State: "Failed", Connection: domain.Connection{Kind: "ec2"}}}
	m := &Manager{options: Options{Terminal: term}, runs: map[string]*entry{id: en}}
	if err := m.CloseShell(context.Background(), id); err == nil || !term.Has(id) {
		t.Fatal("local cleanup failure removed screen")
	}
}

func TestRecoverCompletedTunnelStopIsIdempotent(t *testing.T) {
	for _, state := range []string{"Stopped", "Ended", "Failed"} {
		t.Run(state, func(t *testing.T) {
			c, role, snapshot, cli := fixture(t)
			id, dir := runID(), t.TempDir()
			reg := registration{Run: domain.Run{ID: id, Connection: c, Profile: role.Profile, Identity: role.Identity, Expiration: snapshot.Expiration, State: state, RemoteEnded: state == "Failed", Error: "original cleanup diagnostic"}, PID: os.Getpid(), Heartbeat: time.Now().Add(-time.Hour)}
			if err := ipc.WriteJSON(filepath.Join(dir, "runtime", id+".json"), reg); err != nil {
				t.Fatal(err)
			}
			m, err := NewWithOptions(cli, dir, "/fake", &bookmarks{}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Shutdown(context.Background())
			if got := m.List()[0].State; got != state {
				t.Fatalf("recovered completed %s tunnel as %s", state, got)
			}
			if err := m.Stop(context.Background(), id); err != nil {
				t.Fatalf("stopping already completed %s tunnel after restart: %v", state, err)
			}
			runs := m.List()
			if len(runs) != 1 || runs[0].State != state || runs[0].Error != reg.Run.Error || runs[0].RemoteEnded != reg.Run.RemoteEnded {
				t.Fatalf("recovery or redundant stop changed completion evidence: %+v", runs)
			}
			if m.OwnsTunnel(id) || len(cli.terminations) != 0 {
				t.Fatal("recovery claimed ownership or terminated an external session")
			}
		})
	}
}

func ec2Fixture(t *testing.T) (domain.Connection, domain.ManagedRole, domain.Snapshot, *fakeCLI) {
	t.Helper()
	c, role, snapshot, cli := fixture(t)
	c.Kind, c.InstanceID = "ec2", c.RelayInstanceID
	c.Endpoint, c.RelayInstanceID, c.RelayRegion = "", "", ""
	c.DBPort, c.LocalPort = 0, 0
	return c, role, snapshot, cli
}

func TestReopenStartsFreshRunFromFrozenRunAndKeepsHistory(t *testing.T) {
	c, role, snapshot, cli := ec2Fixture(t)
	term := &ownedHelperTerminal{helperTerminal: helperTerminal{cli: cli}}
	defer term.close()
	m := manager(t, cli, &bookmarks{}, Options{Terminal: term, Attempts: 1000, Interval: 5 * time.Millisecond})
	run, err := m.Open(context.Background(), c, role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// The SSM client dies on its own; the helper records the end and exits.
	cli.mu.Lock()
	child := cli.children[run.SessionID]
	cli.mu.Unlock()
	_ = child.Stop()
	term.wg.Wait()
	term.exited = true
	again, err := m.Reopen(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == run.ID || again.State != "Connected" || again.SessionID == run.SessionID {
		t.Fatalf("reopen did not create a fresh connected run: %+v", again)
	}
	if again.Profile.Name != run.Profile.Name || again.Identity != run.Identity || again.Connection.InstanceID != run.Connection.InstanceID || again.Connection.Region != run.Connection.Region {
		t.Fatal("reopened run changed identity or destination")
	}
	if term.Has(run.ID) || !term.Has(again.ID) {
		t.Fatal("dead screen kept or replacement screen missing")
	}
	states := map[string]string{}
	for _, r := range m.List() {
		states[r.ID] = r.State
	}
	if states[run.ID] != "Ended" || states[again.ID] != "Connected" {
		t.Fatalf("states after reopen: %v", states)
	}
	cli.mu.Lock()
	defer cli.mu.Unlock()
	if len(cli.terminations) != 0 {
		t.Fatal("reopen terminated an unconfirmed remote session")
	}
	if len(cli.snapshots) != 2 || cli.snapshots[1].AccessKeyID != snapshot.AccessKeyID || cli.snapshots[1].SessionToken != snapshot.SessionToken {
		t.Fatal("reopen did not reuse the frozen credential snapshot")
	}
}

func TestReopenRefusesExpiredSnapshotAndLiveShell(t *testing.T) {
	c, role, snapshot, cli := ec2Fixture(t)
	term := &ownedHelperTerminal{helperTerminal: helperTerminal{cli: cli}}
	defer term.close()
	m := manager(t, cli, &bookmarks{}, Options{Terminal: term, Attempts: 1000, Interval: 5 * time.Millisecond})
	run, err := m.Open(context.Background(), c, role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reopen(context.Background(), run.ID); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("live shell reopened: %v", err)
	}
	en, _ := m.find(run.ID)
	en.mu.Lock()
	en.snapshot.Expiration = time.Now().Add(-time.Minute)
	en.run.State = "Ended"
	en.mu.Unlock()
	term.exited = true
	if _, err := m.Reopen(context.Background(), run.ID); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired snapshot reopened: %v", err)
	}
	term.mu.Lock()
	launched := term.launched
	term.mu.Unlock()
	if len(m.List()) != 1 || launched != 1 {
		t.Fatal("refused reopen still created a run")
	}
	if _, err := m.Reopen(context.Background(), "not-owned"); err == nil {
		t.Fatal("unowned run accepted")
	}
}

func TestRecheckOnEndedShellKeepsEndedState(t *testing.T) {
	_, _, _, cli := fixture(t)
	id := runID()
	en := &entry{run: domain.Run{ID: id, State: "Ended", Error: "official SSM client exited; remote termination is unconfirmed", Connection: domain.Connection{Kind: "ec2"}}, endpoint: ipc.Endpoint{Address: "unavailable"}}
	m := &Manager{cli: cli, runs: map[string]*entry{id: en}}
	if err := m.Recheck(context.Background(), id); err == nil {
		t.Fatal("ended shell rechecked as if live")
	}
	r := m.List()[0]
	if r.State != "Ended" || !strings.Contains(r.Error, "official SSM client exited") {
		t.Fatalf("recheck rewrote the ended record: %s %q", r.State, r.Error)
	}
}

func TestReopenRefusedWhenRoleCredentialsUnusable(t *testing.T) {
	c, role, snapshot, cli := ec2Fixture(t)
	term := &ownedHelperTerminal{helperTerminal: helperTerminal{cli: cli}}
	defer term.close()
	gate := errors.New("credentials are unpublished; republish them in Credentials before connecting")
	asked := ""
	m := manager(t, cli, &bookmarks{}, Options{Terminal: term, Attempts: 1000, Interval: 5 * time.Millisecond, RecoveryCredentials: func(alias string) (domain.ManagedRole, domain.Snapshot, error) {
		asked = alias
		return domain.ManagedRole{}, domain.Snapshot{}, gate
	}})
	run, err := m.Open(context.Background(), c, role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	cli.mu.Lock()
	child := cli.children[run.SessionID]
	cli.mu.Unlock()
	_ = child.Stop()
	term.wg.Wait()
	term.exited = true
	if _, err := m.Reopen(context.Background(), run.ID); err == nil || !strings.Contains(err.Error(), "unpublished") {
		t.Fatalf("reopen with unusable credentials: %v", err)
	}
	if asked != role.Alias {
		t.Fatalf("gate consulted for %q, want %q", asked, role.Alias)
	}
	term.mu.Lock()
	launched := term.launched
	term.mu.Unlock()
	if launched != 1 || !term.Has(run.ID) {
		t.Fatal("refused reopen launched a shell or dropped the ended screen")
	}
}
