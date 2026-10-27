// Package sessions binds every run to an immutable credential snapshot and
// correlates only exact reason/owner/target/time metadata returned by SSM.
package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/database"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/ipc"
	"github.com/ThamalDilanka/easy-sso/internal/launch"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// Session creation only persists a successfully established connection.
type bookmarkStore interface {
	Save(domain.Connection) error
}

type Options struct {
	RecoveryCredentials func(string) (domain.ManagedRole, domain.Snapshot, error)
	Terminal            terminal.Adapter
	Window              terminal.Adapter
	Attempts            int
	Interval            time.Duration
	Probe               func(int, domain.Child) (bool, error)
	// Launcher finds and starts DataGrip or Compass; nil selects launch.New().
	Launcher launch.Launcher
}
type entry struct {
	mu            sync.Mutex
	run           domain.Run
	snapshot      domain.Snapshot
	child         domain.Child
	endpoint      ipc.Endpoint
	handle        string
	saved         bool
	cancel        context.CancelFunc
	clientDone    <-chan struct{}
	keepTunnel    bool
	clientClosing bool
	// verified is set only once VerifyTLS passed for the current docdb-iam
	// connection; a launched client relies on it for the hostname check.
	verified      bool
	recovered     bool
	closedLocally bool
	recoveryAfter time.Time
}
type registration struct {
	Run       domain.Run   `json:"run"`
	Endpoint  ipc.Endpoint `json:"endpoint"`
	Heartbeat time.Time    `json:"heartbeat"`
	PID       int          `json:"pid"`
}
type startup struct {
	Run                                  domain.Run
	Snapshot                             domain.Snapshot
	CLIPath, ConfigPath, CredentialsPath string
}
type Manager struct {
	dbOpen               sync.Mutex // serialize local-port acquisition and attachment
	cli                  domain.SessionCLI
	stateDir, executable string
	connections          bookmarkStore
	mu                   sync.Mutex
	runtimeMu            sync.Mutex
	runs                 map[string]*entry
	options              Options
	verifyTLS            func(context.Context, int, string, string) error
	exportCA             func(context.Context, string, bool) (domain.CAExport, error)
	terminalError        error
	done                 chan struct{}
	refreshDone          chan struct{}
	close                sync.Once
	opening              sync.WaitGroup
	dbWorkers            sync.WaitGroup
	closing              bool
}

func New(cli domain.SessionCLI, stateDir, executable string, connections bookmarkStore) (*Manager, error) {
	return NewWithOptions(cli, stateDir, executable, connections, Options{})
}
func NewWithOptions(cli domain.SessionCLI, stateDir, executable string, connections bookmarkStore, o Options) (*Manager, error) {
	dir := filepath.Join(stateDir, "runtime")
	if e := ipc.PrivateDir(dir); e != nil {
		return nil, e
	}
	m := &Manager{cli: cli, stateDir: stateDir, executable: executable, connections: connections, runs: map[string]*entry{}, options: o, verifyTLS: database.VerifyTLS, exportCA: database.ExportCAToWindows, done: make(chan struct{}), refreshDone: make(chan struct{})}
	if o.Terminal == nil {
		m.options.Terminal, m.terminalError = terminal.Detect()
	}
	if m.options.Attempts == 0 {
		m.options.Attempts = 20
	}
	if m.options.Interval == 0 {
		m.options.Interval = 500 * time.Millisecond
	}
	if m.options.Probe == nil {
		m.options.Probe = probeListener
	}
	if m.options.Launcher == nil {
		m.options.Launcher = launch.New()
	}
	m.recover()
	// A crash can leave a private mongosh home behind; a recorded live run keeps its own.
	database.SweepMongoHomes(stateDir, m.liveRun)
	go m.refresh()
	return m, nil
}

// liveRun reports whether a recorded run may still own local files.
func (m *Manager) liveRun(id string) bool {
	m.mu.Lock()
	en, ok := m.runs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	return en.run.State != "Stopped" && en.run.State != "Ended" && en.run.State != "Failed"
}
func (m *Manager) List() []domain.Run {
	m.mu.Lock()
	entries := make([]*entry, 0, len(m.runs))
	for _, e := range m.runs {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	out := make([]domain.Run, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		out = append(out, copyRun(e.run))
		e.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}
func copyRun(r domain.Run) domain.Run {
	r.Profile.Chain = append([]string(nil), r.Profile.Chain...)
	if r.Connection.LastUsedAt != nil {
		t := *r.Connection.LastUsedAt
		r.Connection.LastUsedAt = &t
	}
	return r
}
func runID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func (m *Manager) Open(ctx context.Context, c domain.Connection, role domain.ManagedRole, s domain.Snapshot) (domain.Run, error) {
	return m.open(ctx, c, role, s, m.options.Terminal)
}

// OpenWindow creates a new independent shell, never transfers an owned PTY.
func (m *Manager) OpenWindow(ctx context.Context, c domain.Connection, role domain.ManagedRole, s domain.Snapshot) (domain.Run, error) {
	if c.Kind != "ec2" {
		return domain.Run{}, errors.New("separate windows are only available for EC2 shells")
	}
	adapter := m.options.Window
	if adapter == nil {
		var err error
		adapter, err = terminal.Detect()
		if err != nil {
			return domain.Run{}, err
		}
	}
	return m.open(ctx, c, role, s, adapter)
}

func (m *Manager) open(ctx context.Context, c domain.Connection, role domain.ManagedRole, s domain.Snapshot, adapter terminal.Adapter) (domain.Run, error) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return domain.Run{}, errors.New("workspace is shutting down")
	}
	m.opening.Add(1)
	m.mu.Unlock()
	defer m.opening.Done()
	if c.Kind == "db" {
		m.dbOpen.Lock()
		defer m.dbOpen.Unlock()
	}
	if e := connections.Validate(c); e != nil {
		return domain.Run{}, e
	}
	if !connections.CompatibleRegion(c.Partition, c.Region) || (c.Kind == "db" && !connections.CompatibleRegion(c.Partition, c.RelayRegion)) {
		return domain.Run{}, errors.New("connection region is incompatible with the selected AWS partition")
	}
	if role.Status != "Active" && role.Status != "active" {
		return domain.Run{}, errors.New("activate this role before connecting")
	}
	if e := awscli.ValidateSnapshot(s); e != nil {
		return domain.Run{}, e
	}
	if !role.Expiration.After(time.Now()) || !s.Expiration.Equal(role.Expiration) {
		return domain.Run{}, errors.New("role credentials expired or snapshot does not match the approved role")
	}
	if c.AccountID != role.Profile.AccountID || c.Partition != role.Profile.Partition {
		return domain.Run{}, errors.New("connection account/partition does not match the selected role")
	}
	id, e := m.cli.Identity(ctx, s, region(c))
	if e != nil {
		return domain.Run{}, e
	}
	if e = profiles.ValidateIdentity(role.Profile, id); e != nil {
		return domain.Run{}, errors.New("connection blocked: resolved AWS identity does not match this account and role")
	}
	if id.Account != role.Identity.Account || id.ARN != role.Identity.ARN || id.UserID != role.Identity.UserID {
		return domain.Run{}, errors.New("connection blocked: frozen role identity changed")
	}
	var clientPath, caPath string
	if c.IAMDatabase() {
		if _, ok := adapter.(databaseTerminal); !ok {
			return domain.Run{}, errors.New("PostgreSQL IAM login requires an embedded terminal on Linux, WSL or macOS")
		}
		if _, ok := m.cli.(databaseSigner); !ok {
			return domain.Run{}, errors.New("IAM token signing unavailable")
		}
		clientPath, caPath, e = database.Prepare(ctx, c, m.stateDir)
		if e != nil {
			return domain.Run{}, e
		}
	} else if c.DocDBIAM() {
		// mongosh reads the frozen keys from its environment; no token is signed.
		if _, ok := adapter.(databaseTerminal); !ok {
			return domain.Run{}, errors.New("DocumentDB IAM login needs Linux, macOS or WSL")
		}
		clientPath, caPath, e = database.PrepareMongo(ctx, c, m.stateDir)
		if e != nil {
			return domain.Run{}, e
		}
	}
	if c.Kind == "db" {
		if reused, found, err := m.reuseDatabase(ctx, c, role, s, id, adapter, clientPath, caPath); found || err != nil {
			return reused, err
		}
		l, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(c.LocalPort)))
		if e != nil {
			return domain.Run{}, fmt.Errorf("port %d is in use; choose another local port", c.LocalPort)
		}
		_ = l.Close()
	}
	label := c.AccountID
	if settings, e := store.LoadSettings(m.stateDir); e == nil && settings.AccountAliases[c.AccountID] != "" {
		label = settings.AccountAliases[c.AccountID]
	}
	run := domain.Run{ID: runID(), Connection: c, Profile: role.Profile, Alias: role.Alias, Identity: id, Expiration: s.Expiration, State: "Reviewed", Title: terminal.CleanTitle(c.Name + " · " + label), StartedAt: time.Now().UTC()}
	run = copyRun(run)
	en := &entry{run: run, snapshot: s}
	m.mu.Lock()
	ordinal := 1
	for _, other := range m.runs {
		other.mu.Lock()
		if other.run.Connection.Kind == "ec2" && other.run.State != "Ended" && other.run.State != "Failed" {
			used := 0
			if other.run.Title == run.Title {
				used = 1
			} else if strings.HasPrefix(other.run.Title, run.Title+" #") {
				used, _ = strconv.Atoi(strings.TrimPrefix(other.run.Title, run.Title+" #"))
			}
			if used >= ordinal {
				ordinal = used + 1
			}
		}
		other.mu.Unlock()
	}
	if c.Kind == "ec2" && ordinal > 1 {
		en.run.Title = fmt.Sprintf("%s #%d", run.Title, ordinal)
	}
	m.runs[run.ID] = en
	m.mu.Unlock()
	if c.Kind == "ec2" {
		e = m.openShell(ctx, en, adapter)
	} else {
		e = m.openTunnel(ctx, en)
		if c.DatabaseClient() {
			if e == nil {
				e = m.openDatabaseClient(ctx, en, adapter.(databaseTerminal), clientPath, caPath)
			} else {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				cleanupErr := m.stop(cleanup, run.ID)
				cancel()
				state, message := "Failed", e.Error()
				if cleanupErr != nil {
					message += "; " + cleanupErr.Error()
				}
				en.mu.Lock()
				if en.run.State == "Unknown" {
					state = "Unknown"
				}
				en.mu.Unlock()
				m.set(en, state, message)
			}
		}
	}
	en.mu.Lock()
	result := copyRun(en.run)
	en.mu.Unlock()
	return result, e
}
func region(c domain.Connection) string {
	if c.Kind == "db" {
		return c.RelayRegion
	}
	return c.Region
}
func target(c domain.Connection) string {
	if c.Kind == "db" {
		return c.RelayInstanceID
	}
	return c.InstanceID
}

// tunnelDocument is the bookmark's tunnel document, or the AWS default.
func tunnelDocument(c domain.Connection) string {
	if c.DocumentName != "" {
		return c.DocumentName
	}
	return connections.DefaultTunnelDocument
}
func arguments(r domain.Run) []string {
	a := []string{"ssm", "start-session", "--target", target(r.Connection), "--region", region(r.Connection), "--reason", "easy-sso:" + r.ID}
	if r.Connection.Kind == "db" {
		b, _ := json.Marshal(map[string][]string{"host": {r.Connection.Endpoint}, "portNumber": {strconv.Itoa(r.Connection.DBPort)}, "localPortNumber": {strconv.Itoa(r.Connection.LocalPort)}})
		a = append(a, "--document-name", tunnelDocument(r.Connection), "--parameters", string(b))
	} else if r.Connection.DocumentName != "" {
		a = append(a, "--document-name", r.Connection.DocumentName)
	}
	return a
}
func (m *Manager) set(en *entry, state, msg string) {
	en.mu.Lock()
	en.run.State = state
	en.run.Error = msg
	en.mu.Unlock()
	m.persistTunnel(en)
}
func (m *Manager) openTunnel(ctx context.Context, en *entry) error {
	en.mu.Lock()
	frozen := copyRun(en.run)
	snapshot := en.snapshot
	en.mu.Unlock()
	m.set(en, "Starting", "")
	life, cancel := context.WithCancel(context.Background())
	diagnostic := &startupDiagnostic{document: tunnelDocument(frozen.Connection)}
	child, e := m.cli.Start(life, snapshot, arguments(frozen), nil, diagnostic, diagnostic)
	if e != nil {
		cancel()
		m.set(en, "Failed", e.Error())
		return e
	}
	en.mu.Lock()
	en.child = child
	en.cancel = cancel
	en.mu.Unlock()
	m.dbWorkers.Add(1)
	go func() { defer m.dbWorkers.Done(); m.watch(en, child, diagnostic) }()
	if e = m.confirm(ctx, en); e != nil {
		if ctx.Err() != nil {
			_ = m.stop(context.Background(), frozen.ID)
		}
		m.persistTunnel(en)
		return e
	}
	m.set(en, "SSMConnected", "")
	for i := 0; i < m.options.Attempts; i++ {
		en.mu.Lock()
		st := en.run.State
		en.mu.Unlock()
		if st == "Failed" || st == "Stopped" {
			return errors.New("forwarding process ended before listener readiness")
		}
		ready, e := m.options.Probe(frozen.Connection.LocalPort, child)
		if e != nil {
			_ = m.stop(ctx, frozen.ID)
			m.set(en, "Failed", e.Error())
			return e
		}
		if ready {
			m.set(en, "Listening", "")
			if frozen.Connection.DatabaseClient() {
				return nil
			}
			return m.save(en)
		}
		if e = pause(ctx, m.options.Interval); e != nil {
			_ = m.stop(context.Background(), frozen.ID)
			return e
		}
	}
	_ = m.stop(context.Background(), frozen.ID)
	m.set(en, "Failed", "forwarding listener was not confirmed; check plugin support and port conflicts")
	return errors.New("forwarding listener was not confirmed")
}
func (m *Manager) watch(en *entry, child domain.Child, diagnostic *startupDiagnostic) {
	e := child.Wait()
	en.mu.Lock()
	if en.run.State == "Stopping" || en.run.State == "Stopped" || en.run.State == "Ending" || en.run.State == "Ended" || en.run.State == "Failed" {
		en.mu.Unlock()
		return
	}
	if en.run.Connection.Kind == "db" {
		en.run.State = "Stopped"
	} else {
		en.run.State = "Ended"
	}
	if e != nil {
		en.run.State = "Failed"
		en.run.Error = "official SSM client exited unexpectedly; remote termination is unconfirmed"
	}
	if message := diagnostic.tunnelFailure(); message != "" {
		en.run.State, en.run.Error = "Failed", message
	}
	en.mu.Unlock()
	m.persistTunnel(en)
}
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (m *Manager) confirm(ctx context.Context, en *entry) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for i := 0; i < m.options.Attempts; i++ {
		en.mu.Lock()
		state := en.run.State
		message := en.run.Error
		en.mu.Unlock()
		if state == "Ended" || state == "Stopped" || state == "Failed" || state == "Stopping" || state == "Ending" {
			if message != "" {
				return errors.New(message)
			}
			return errors.New("SSM client ended before a confirmed connection")
		}
		e := m.correlate(ctx, en)
		if e == nil {
			en.mu.Lock()
			known := en.run.SessionID != ""
			en.mu.Unlock()
			if known {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i < m.options.Attempts-1 {
			if e = pause(ctx, m.options.Interval); e != nil {
				return e
			}
		}
	}
	en.mu.Lock()
	if en.run.State == "Ended" || en.run.State == "Failed" || en.run.State == "Stopped" {
		message := en.run.Error
		en.mu.Unlock()
		if message != "" {
			return errors.New(message)
		}
		return errors.New("SSM client ended before a confirmed connection")
	}
	en.run.State = "Unknown"
	en.run.Error = "SSM session not confirmed; recheck metadata permissions and connectivity"
	en.mu.Unlock()
	m.persistTunnel(en)
	return errors.New("SSM session not confirmed; no bookmark saved")
}
func (m *Manager) correlate(ctx context.Context, en *entry) error {
	en.mu.Lock()
	r := copyRun(en.run)
	s := en.snapshot
	en.mu.Unlock()
	filters := []map[string]string{{"key": "Target", "value": target(r.Connection)}, {"key": "Owner", "value": r.Identity.ARN}, {"key": "InvokedAfter", "value": r.StartedAt.Add(-30 * time.Second).Format(time.RFC3339)}, {"key": "InvokedBefore", "value": r.StartedAt.Add(5 * time.Minute).Format(time.RFC3339)}}
	if r.SessionID != "" {
		filters = []map[string]string{{"key": "SessionId", "value": r.SessionID}}
	}
	b, _ := json.Marshal(filters)
	metadata, e := m.cli.Describe(ctx, s, region(r.Connection), []string{string(b)})
	if e != nil {
		return e
	}
	matches := []domain.SessionMetadata{}
	for _, v := range metadata {
		if v.SessionID == "" || v.Reason != "easy-sso:"+r.ID || v.Target != target(r.Connection) || v.Owner != r.Identity.ARN || v.StartDate.Before(r.StartedAt.Add(-30*time.Second)) || v.StartDate.After(r.StartedAt.Add(5*time.Minute)) || (r.SessionID != "" && v.SessionID != r.SessionID) {
			continue
		}
		matches = append(matches, v)
	}
	if len(matches) != 1 {
		return errors.New("exact SSM session correlation unavailable or ambiguous")
	}
	v := matches[0]
	en.mu.Lock()
	if en.run.State == "Ended" || en.run.State == "Stopped" || en.run.State == "Failed" || en.run.State == "Stopping" || en.run.State == "Ending" {
		en.mu.Unlock()
		return errors.New("run has ended")
	}
	en.run.SessionID = v.SessionID
	switch v.Status {
	case "Connected":
		if r.Connection.Kind == "ec2" {
			en.run.State = "Connected"
		} else {
			en.run.State = "SSMConnected"
		}
		en.run.Error = ""
	case "Connecting":
		en.run.State = "Connecting"
	case "Terminated":
		en.run.State = "Ended"
		en.run.RemoteEnded = true
	default:
		en.run.State = "Unknown"
	}
	en.mu.Unlock()
	if v.Status != "Connected" {
		return errors.New("SSM session is not connected")
	}
	return nil
}
func (m *Manager) save(en *entry) error {
	if m.connections == nil {
		return nil
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	if en.saved {
		return nil
	}
	c := en.run.Connection
	now := time.Now().UTC()
	c.LastUsedAt = &now
	c.LastUsedProfile = en.run.Profile.Name
	c.LastUsedRole = en.run.Profile.Role
	if e := m.connections.Save(c); e != nil {
		en.run.Error = "connection established, but bookmark could not be saved"
		return errors.New(en.run.Error)
	}
	en.saved = true
	en.run.Connection = c
	return nil
}
func (m *Manager) find(id string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.runs[id]
	if e == nil {
		return nil, errors.New("unknown application run")
	}
	return e, nil
}
func (m *Manager) Stop(ctx context.Context, id string) error {
	en, err := m.find(id)
	if err != nil {
		return err
	}
	en.mu.Lock()
	db := en.run.Connection.Kind == "db"
	en.mu.Unlock()
	if db {
		m.dbOpen.Lock()
		defer m.dbOpen.Unlock()
	}
	return m.stop(ctx, id)
}

func (m *Manager) stop(ctx context.Context, id string) error {
	en, e := m.find(id)
	if e != nil {
		return e
	}
	en.mu.Lock()
	r := en.run
	ep := en.endpoint
	child := en.child
	cancel := en.cancel
	s := en.snapshot
	clientDone := en.clientDone
	recovered := en.recovered
	en.clientClosing = true
	en.mu.Unlock()
	if recovered && m.options.RecoveryCredentials != nil {
		return m.recoverTunnel(ctx, en, true)
	}
	if clientDone != nil {
		if term, ok := m.options.Terminal.(databaseTerminal); ok {
			if err := term.Close(id); err != nil {
				return err
			}
		}
	}
	// Already completed records need no process ownership or credentials to
	// acknowledge another Stop. Preserve any unconfirmed remote-cleanup warning.
	if r.State == "Stopped" || r.State == "Ended" || (r.State == "Failed" && r.RemoteEnded) {
		return nil
	}
	if ep.Address != "" {
		var result domain.Run
		if e = ipc.Call(ep, "stop", &result); e != nil {
			m.set(en, "Unknown", "helper unavailable; remote termination is unconfirmed")
			return e
		}
		en.mu.Lock()
		if !sameBinding(en.run, result) {
			en.mu.Unlock()
			m.set(en, "Unknown", "helper response did not match the frozen run identity")
			return errors.New("helper identity mismatch")
		}
		en.run = copyRun(result)
		en.mu.Unlock()
		if !result.RemoteEnded {
			return errors.New("local session ended; remote termination is unconfirmed")
		}
		return nil
	}
	if child == nil && s.AccessKeyID == "" {
		// Do not overwrite a registration that another instance may still own.
		return errors.New("this run is not owned by this easy-sso instance; stop it in the instance that opened it. If that instance has exited, verify the saved SSM session ID and local process before cleanup")
	}
	state, end := "Stopping", "Stopped"
	if r.Connection.Kind == "ec2" {
		state, end = "Ending", "Ended"
	}
	m.set(en, state, "")
	var remoteErr error
	if r.SessionID != "" {
		remoteErr = m.cli.Terminate(ctx, s, region(r.Connection), r.SessionID)
	} else {
		remoteErr = errors.New("exact remote SessionId unknown")
	}
	var localErr error
	if child != nil {
		localErr = child.Stop()
	}
	if cancel != nil {
		cancel()
	}
	if child != nil {
		waited := make(chan struct{})
		go func() { _ = child.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			localErr = errors.New("owned process cleanup timed out")
		}
	}
	en.mu.Lock()
	en.run.RemoteEnded = remoteErr == nil && r.SessionID != ""
	en.run.State = end
	if remoteErr != nil {
		en.run.Error = "local process stopped; remote termination is unconfirmed"
	}
	if localErr != nil {
		en.run.State = "Unknown"
		en.run.Error = "owned process cleanup failed; remote termination status shown separately"
	}
	en.mu.Unlock()
	m.persistTunnel(en)
	if localErr != nil {
		return errors.New("owned process cleanup failed")
	}
	if remoteErr != nil {
		return errors.New("local process stopped; remote termination is unconfirmed")
	}
	return nil
}
func (m *Manager) Focus(id string) error {
	en, e := m.find(id)
	if e != nil {
		return e
	}
	en.mu.Lock()
	handle := en.handle
	en.mu.Unlock()
	if m.options.Terminal == nil || !m.options.Terminal.Capabilities().FocusExisting {
		return errors.New("terminal focus unavailable; switch using terminal tabs")
	}
	return m.options.Terminal.Focus(handle)
}
func (m *Manager) Recheck(ctx context.Context, id string) error {
	en, e := m.find(id)
	if e != nil {
		return e
	}
	en.mu.Lock()
	db := en.run.Connection.Kind == "db"
	state := en.run.State
	en.mu.Unlock()
	// An ended shell has no helper left to ask; its record is history now.
	if !db && (state == "Ended" || state == "Failed") {
		return errors.New("shell has ended; reconnect or close its tab")
	}
	if db {
		m.dbOpen.Lock()
		defer m.dbOpen.Unlock()
	}
	en.mu.Lock()
	frozen := copyRun(en.run)
	ep := en.endpoint
	has := en.snapshot.AccessKeyID != ""
	recovered := en.recovered
	en.mu.Unlock()
	if recovered && m.options.RecoveryCredentials != nil {
		return m.recoverTunnel(ctx, en, false)
	}
	if ep.Address != "" {
		var result domain.Run
		if e = ipc.Call(ep, "recheck", &result); e != nil {
			m.set(en, "Unknown", "helper unavailable; session status unknown")
			return e
		}
		en.mu.Lock()
		if !sameBinding(en.run, result) {
			en.mu.Unlock()
			m.set(en, "Unknown", "helper response did not match the frozen run identity")
			return errors.New("helper identity mismatch")
		}
		en.run = copyRun(result)
		en.mu.Unlock()
		if result.State == "Connected" {
			return m.save(en)
		}
		return errors.New("SSM session is not confirmed connected")
	}
	if !has {
		return errors.New("recovered tunnel has no credential snapshot; verify remotely with a new authorized role")
	}
	if e = m.correlate(ctx, en); e != nil {
		m.set(en, "Unknown", "SSM metadata unavailable; session status unknown")
		return e
	}
	if frozen.Connection.Kind == "db" {
		en.mu.Lock()
		child := en.child
		en.mu.Unlock()
		ready, e := m.options.Probe(frozen.Connection.LocalPort, child)
		if e != nil {
			return e
		}
		if !ready {
			m.set(en, "Unknown", "local listener not confirmed")
			return errors.New("local listener not confirmed")
		}
		if frozen.Connection.DatabaseClient() {
			en.mu.Lock()
			en.run.State = frozen.State
			en.run.Error = frozen.Error
			en.mu.Unlock()
			return nil
		}
		m.set(en, "Listening", "")
	}
	return m.save(en)
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.close.Do(func() {
		m.mu.Lock()
		m.closing = true
		close(m.done)
		m.mu.Unlock()
		<-m.refreshDone
		m.opening.Wait()
	})
	var errs []error
	for _, r := range m.List() {
		if embedded, ok := m.options.Terminal.(interface {
			Has(string) bool
			Close(string) error
		}); ok && embedded.Has(r.ID) {
			if r.State != "Ended" && r.State != "Failed" {
				if err := m.Stop(ctx, r.ID); err != nil {
					errs = append(errs, err)
				}
			}
			if err := embedded.Close(r.ID); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if r.Connection.Kind == "db" && r.State != "Stopped" {
			en, _ := m.find(r.ID)
			en.mu.Lock()
			owned := en.child != nil || en.snapshot.AccessKeyID != ""
			en.mu.Unlock()
			if !owned {
				continue
			}
			if e := m.Stop(ctx, r.ID); e != nil {
				errs = append(errs, e)
			}
		}
	}
	if len(errs) == 0 {
		m.dbWorkers.Wait()
	}
	return errors.Join(errs...)
}

// OwnsTunnel reports local ownership without trusting persisted process IDs.
func (m *Manager) OwnsTunnel(id string) bool {
	en, err := m.find(id)
	if err != nil {
		return false
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	return en.run.Connection.Kind == "db" && (en.child != nil || en.snapshot.AccessKeyID != "")
}

// CloseShell closes only a PTY owned by this manager. An uncertain local stop
// keeps the screen accessible instead of hiding a potentially running shell.
func (m *Manager) CloseShell(ctx context.Context, id string) error {
	terminal, ok := m.options.Terminal.(interface {
		Has(string) bool
		Close(string) error
	})
	if !ok || !terminal.Has(id) {
		return errors.New("shell is not owned by this workspace")
	}
	en, err := m.find(id)
	if err != nil {
		return err
	}
	en.mu.Lock()
	run := copyRun(en.run)
	en.mu.Unlock()
	if run.Connection.Kind == "db" {
		m.dbOpen.Lock()
		defer m.dbOpen.Unlock()
		return m.closeDatabaseClient(ctx, en)
	}
	exited := func() bool {
		process, ok := terminal.(interface{ Exited(string) bool })
		return ok && process.Exited(id)
	}
	var stopErr error
	if run.State != "Ended" && run.State != "Failed" && !exited() {
		stopErr = m.stop(ctx, id)
		en.mu.Lock()
		run = copyRun(en.run)
		en.mu.Unlock()
		if run.State != "Ended" && run.State != "Failed" && !exited() {
			return errors.New("shell remains open because local cleanup is unconfirmed; retry close or check the terminal")
		}
	}
	if run.State != "Ended" && run.State != "Failed" {
		m.set(en, "Ended", "local terminal exited; remote termination is unconfirmed")
		stopErr = errors.New("local terminal closed; remote termination is unconfirmed")
	}
	if err := terminal.Close(id); err != nil {
		return fmt.Errorf("could not close local terminal: %w", err)
	}
	return stopErr
}

// Reopen starts a fresh shell for a run whose local SSM client has ended,
// reusing its frozen connection, profile, identity and credential snapshot.
// The ended record stays in history, and nothing is re-resolved from the
// currently selected profile.
func (m *Manager) Reopen(ctx context.Context, id string) (domain.Run, error) {
	terminal, ok := m.options.Terminal.(interface {
		Has(string) bool
		Exited(string) bool
	})
	if !ok || !terminal.Has(id) {
		return domain.Run{}, errors.New("shell is not owned by this workspace")
	}
	en, err := m.find(id)
	if err != nil {
		return domain.Run{}, err
	}
	en.mu.Lock()
	frozen := copyRun(en.run)
	snapshot := en.snapshot
	en.mu.Unlock()
	if frozen.Connection.Kind != "ec2" {
		return domain.Run{}, errors.New("only EC2 shells can be reopened")
	}
	ended := frozen.State == "Ended" || frozen.State == "Failed"
	if !ended && !terminal.Exited(id) {
		return domain.Run{}, errors.New("shell is still running; close it before reconnecting")
	}
	if !ended {
		m.set(en, "Ended", "local SSM client exited; remote termination is unconfirmed")
	}
	if snapshot.AccessKeyID == "" || !snapshot.Expiration.After(time.Now()) {
		return domain.Run{}, errors.New("frozen credentials have expired; renew the role and connect again from EC2")
	}
	// The role must still be usable in Credentials: an unpublished or removed role
	// opens no new shells, not even from its own frozen snapshot.
	if m.options.RecoveryCredentials != nil {
		if _, _, err := m.options.RecoveryCredentials(frozen.Alias); err != nil {
			return domain.Run{}, fmt.Errorf("reconnect refused: %w", err)
		}
	}
	role := domain.ManagedRole{Alias: frozen.Alias, Profile: frozen.Profile, Identity: frozen.Identity, Expiration: snapshot.Expiration, Status: "Active"}
	run, err := m.open(ctx, frozen.Connection, role, snapshot, m.options.Terminal)
	if err != nil {
		return run, err
	}
	// Release the dead screen only once its replacement exists.
	_ = m.CloseShell(ctx, id)
	return run, nil
}
