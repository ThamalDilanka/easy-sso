package sessions

import (
	"context"
	"errors"
	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/ipc"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

func (m *Manager) openShell(ctx context.Context, en *entry, adapter terminal.Adapter) error {
	en.mu.Lock()
	frozen := copyRun(en.run)
	snapshot := en.snapshot
	en.mu.Unlock()
	if adapter == nil {
		err := m.terminalError
		if err == nil {
			err = errors.New("terminal adapter unavailable")
		}
		m.set(en, "Failed", err.Error())
		return err
	}
	cfg, ok := m.cli.(interface {
		HelperConfig() (string, string, string)
	})
	if !ok {
		m.set(en, "Failed", "CLI helper configuration unavailable")
		return errors.New("CLI helper configuration unavailable")
	}
	cliPath, config, creds := cfg.HelperConfig()
	data := startup{Run: frozen, Snapshot: snapshot, CLIPath: cliPath, ConfigPath: config, CredentialsPath: creds}
	var consume sync.Mutex
	used := false
	dir := filepath.Join(m.stateDir, "runtime")
	server, e := ipc.Listen(dir, frozen.ID+"-startup", func(action string) (any, error) {
		consume.Lock()
		defer consume.Unlock()
		if action != "start" || used {
			return nil, errors.New("startup exchange already consumed or invalid")
		}
		used = true
		return data, nil
	})
	if e != nil {
		m.set(en, "Failed", "private helper IPC could not be created")
		return e
	}
	defer server.Close()
	path := filepath.Join(dir, frozen.ID+"-startup.json")
	if e = ipc.WriteJSON(path, server.Endpoint); e != nil {
		return e
	}
	defer os.Remove(path)
	m.set(en, "LaunchingTerminal", "")
	handle, e := adapter.Launch(ctx, terminal.Request{Executable: m.executable, StateDir: m.stateDir, RunID: frozen.ID, Title: frozen.Title})
	if e != nil {
		m.set(en, "Failed", e.Error())
		return e
	}
	en.mu.Lock()
	en.handle = handle
	en.mu.Unlock()
	m.set(en, "Connecting", "")
	for i := 0; i < m.options.Attempts; i++ {
		var reg registration
		if ipc.ReadJSON(filepath.Join(dir, frozen.ID+".json"), &reg) == nil && validRegistration(reg, frozen.ID) && sameBinding(frozen, reg.Run) {
			en.mu.Lock()
			en.endpoint = reg.Endpoint
			en.run = copyRun(reg.Run)
			en.mu.Unlock()
			if reg.Run.State == "Connected" {
				return m.save(en)
			}
			if reg.Run.State == "Failed" || reg.Run.State == "Ended" {
				if reg.Run.Error != "" {
					return errors.New(reg.Run.Error)
				}
				return errors.New("SSM client exited before connecting; see terminal output")
			}
		}
		if e = pause(ctx, m.options.Interval); e != nil {
			m.set(en, "Unknown", "startup interrupted; helper connection may still be running")
			return e
		}
	}
	m.set(en, "Unknown", "terminal launched, but the SSM helper connection is unconfirmed")
	return errors.New("SSM helper connection unconfirmed; no bookmark saved")
}
func validRegistration(r registration, id string) bool {
	return uuidPattern.MatchString(id) && r.Run.ID == id && r.Run.Connection.Kind == "ec2" && r.Endpoint.Address != "" && r.Heartbeat.After(time.Now().Add(-10*time.Second))
}
func (m *Manager) recover() {
	files, _ := os.ReadDir(filepath.Join(m.stateDir, "runtime"))
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(f.Name(), ".json")
		if !uuidPattern.MatchString(id) {
			continue
		}
		var reg registration
		if ipc.ReadJSON(filepath.Join(m.stateDir, "runtime", f.Name()), &reg) != nil || reg.Run.ID != id {
			continue
		}
		if validateBinding(reg.Run) != nil {
			continue
		}
		// Completed runs remain history. A live tunnel candidate still requires
		// independent process verification; persisted PIDs never convey ownership.
		en := &entry{run: copyRun(reg.Run), endpoint: reg.Endpoint}
		if reg.Run.Connection.Kind == "db" {
			en.recovered = true
			en.closedLocally = reg.Run.State == "Stopped" || reg.Run.State == "Ended" || reg.Run.State == "Failed"
			en.endpoint = ipc.Endpoint{}
			if reg.Run.State != "Stopped" && reg.Run.State != "Ended" && reg.Run.State != "Failed" {
				en.run.State = "Unknown"
				en.run.Error = "Tunnel recorded by an earlier easy-sso instance; live process and remote session status are unverified."
				if m.options.RecoveryCredentials != nil {
					en.run.State = "Checking"
					en.run.Error = "Checking the saved SSM session with its original profile."
				}
			}
		} else if reg.Run.State != "Ended" && reg.Run.State != "Failed" && !validRegistration(reg, id) {
			en.run.State = "Unknown"
			en.run.Error = "stale helper registration; no process was terminated"
		}
		m.runs[id] = en
	}
}
func (m *Manager) refresh() {
	defer close(m.refreshDone)
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-timer.C:
			m.mu.Lock()
			entries := make([]*entry, 0, len(m.runs))
			for _, en := range m.runs {
				entries = append(entries, en)
			}
			m.mu.Unlock()
			for _, en := range entries {
				en.mu.Lock()
				ep := en.endpoint
				id := en.run.ID
				kind := en.run.Connection.Kind
				state := en.run.State
				en.mu.Unlock()
				if kind != "ec2" || ep.Address == "" || state == "Ended" || state == "Failed" {
					continue
				}
				var reg registration
				if ipc.ReadJSON(filepath.Join(m.stateDir, "runtime", id+".json"), &reg) != nil || !validRegistration(reg, id) {
					m.set(en, "Unknown", "helper heartbeat unavailable; session status unknown")
					continue
				}
				en.mu.Lock()
				bound := sameBinding(en.run, reg.Run)
				en.mu.Unlock()
				if reg.Endpoint != ep || !bound {
					m.set(en, "Unknown", "helper identity or control endpoint changed; review before reconnecting")
					continue
				}
				en.mu.Lock()
				en.run = copyRun(reg.Run)
				en.mu.Unlock()
				if reg.Run.State == "Connected" {
					_ = m.save(en)
				}
			}
		}
	}
}

// RunHelper is the same application's native-terminal entry mode. The startup
// registration contains only a nonce and endpoint; credentials remain in memory.
func RunHelper(ctx context.Context, stateDir, id string) error {
	if !uuidPattern.MatchString(id) {
		return errors.New("invalid run identifier")
	}
	dir := filepath.Join(stateDir, "runtime")
	var ep ipc.Endpoint
	if e := ipc.ReadJSON(filepath.Join(dir, id+"-startup.json"), &ep); e != nil {
		return errors.New("private helper startup registration unavailable")
	}
	var data startup
	if e := ipc.Call(ep, "start", &data); e != nil {
		return e
	}
	if data.Run.ID != id || data.Run.Connection.Kind != "ec2" {
		return errors.New("helper startup run does not match")
	}
	if e := awscli.ValidateSnapshot(data.Snapshot); e != nil {
		return e
	}
	cli, e := awscli.New(data.CLIPath, data.ConfigPath, data.CredentialsPath, stateDir)
	if e != nil {
		return e
	}
	return runHelperWithCLI(ctx, stateDir, data, cli)
}
func runHelperWithCLI(ctx context.Context, stateDir string, data startup, cli domain.SessionCLI) error {
	id := data.Run.ID
	dir := filepath.Join(stateDir, "runtime")
	en := &entry{run: copyRun(data.Run), snapshot: data.Snapshot}
	en.run.State = "Connecting"
	m := &Manager{cli: cli, stateDir: stateDir, runs: map[string]*entry{id: en}, options: Options{Attempts: 20, Interval: 500 * time.Millisecond}}
	path := filepath.Join(dir, id+".json")
	var persistMu sync.Mutex
	persist := func() error {
		persistMu.Lock()
		defer persistMu.Unlock()
		en.mu.Lock()
		reg := registration{Run: copyRun(en.run), Endpoint: en.endpoint, Heartbeat: time.Now().UTC(), PID: os.Getpid()}
		en.mu.Unlock()
		return ipc.WriteJSON(path, reg)
	}
	var control sync.Mutex
	server, e := ipc.Listen(dir, id+"-control", func(action string) (any, error) {
		control.Lock()
		defer control.Unlock()
		switch action {
		case "status":
		case "recheck":
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			e := m.correlate(c, en)
			cancel()
			if e != nil {
				m.set(en, "Unknown", "SSM metadata unavailable; session status unknown")
			}
		case "stop":
			en.mu.Lock()
			saved := en.endpoint
			en.endpoint = ipc.Endpoint{}
			en.mu.Unlock()
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = m.Stop(c, id)
			cancel()
			en.mu.Lock()
			en.endpoint = saved
			en.mu.Unlock()
		default:
			return nil, errors.New("unsupported helper control action")
		}
		_ = persist()
		en.mu.Lock()
		defer en.mu.Unlock()
		return copyRun(en.run), nil
	})
	if e != nil {
		return e
	}
	defer server.Close()
	en.endpoint = server.Endpoint
	if e = persist(); e != nil {
		return e
	}
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	diagnostic := &startupDiagnostic{document: data.Run.Connection.DocumentName}
	child, e := cli.Start(life, data.Snapshot, arguments(data.Run), os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, diagnostic))
	if e != nil {
		m.set(en, "Failed", e.Error())
		_ = persist()
		return e
	}
	en.mu.Lock()
	en.child = child
	en.cancel = cancel
	en.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	confirmed := make(chan struct{})
	go func() { defer close(confirmed); _ = m.confirm(life, en); _ = persist() }()
	defer func() { cancel(); <-confirmed }()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case e := <-done:
			en.mu.Lock()
			if en.run.State != "Ended" {
				en.run.State = "Ended"
				if e != nil {
					if en.run.SessionID == "" {
						en.run.State = "Failed"
						en.run.Error = diagnostic.failure()
					} else {
						en.run.Error = "official SSM client exited; remote termination is unconfirmed"
					}
				}
			}
			en.mu.Unlock()
			_ = persist()
			return nil
		case <-ctx.Done():
			_ = child.Stop()
			m.set(en, "Ended", "helper stopped; remote termination is unconfirmed")
			_ = persist()
			return ctx.Err()
		case <-ticker.C:
			if e = persist(); e != nil {
				_ = child.Stop()
				return errors.New("helper registration cannot be maintained")
			}
		}
	}
}

func (m *Manager) persistTunnel(en *entry) {
	m.runtimeMu.Lock()
	defer m.runtimeMu.Unlock()
	en.mu.Lock()
	if en.run.Connection.Kind != "db" {
		en.mu.Unlock()
		return
	}
	reg := registration{Run: copyRun(en.run), Heartbeat: time.Now().UTC(), PID: os.Getpid()}
	en.mu.Unlock()
	_ = ipc.WriteJSON(filepath.Join(m.stateDir, "runtime", reg.Run.ID+".json"), reg)
}

func sameBinding(a, b domain.Run) bool {
	return a.ID == b.ID && a.Connection.Kind == b.Connection.Kind && a.Connection.AccountID == b.Connection.AccountID && a.Connection.Partition == b.Connection.Partition && a.Connection.Region == b.Connection.Region && a.Connection.RelayRegion == b.Connection.RelayRegion && a.Connection.InstanceID == b.Connection.InstanceID && a.Connection.DocumentName == b.Connection.DocumentName && a.Connection.RelayInstanceID == b.Connection.RelayInstanceID && a.Connection.Endpoint == b.Connection.Endpoint && a.Connection.DBPort == b.Connection.DBPort && a.Connection.LocalPort == b.Connection.LocalPort && a.Connection.AuthMode == b.Connection.AuthMode && a.Connection.Database == b.Connection.Database && a.Connection.DBUser == b.Connection.DBUser && a.Connection.CAFile == b.Connection.CAFile && reflect.DeepEqual(a.Profile, b.Profile) && a.Alias == b.Alias && a.Identity == b.Identity && a.Expiration.Equal(b.Expiration) && a.StartedAt.Equal(b.StartedAt) && a.Title == b.Title && (a.SessionID == "" || a.SessionID == b.SessionID)
}
func validateBinding(r domain.Run) error {
	if !uuidPattern.MatchString(r.ID) || r.Connection.AccountID != r.Identity.Account || r.Connection.Partition != r.Profile.Partition || !connections.CompatibleRegion(r.Connection.Partition, r.Connection.Region) || (r.Connection.Kind == "db" && !connections.CompatibleRegion(r.Connection.Partition, r.Connection.RelayRegion)) {
		return errors.New("invalid recovered run binding")
	}
	if e := connections.Validate(r.Connection); e != nil {
		return e
	}
	return profiles.ValidateIdentity(r.Profile, r.Identity)
}

// Inspect only bounded stderr fragments for known startup error codes. Persist
// a fixed explanation, never raw AWS output, identities, or terminal contents.
type startupDiagnostic struct {
	mu       sync.Mutex
	tail     string
	denied   bool
	document string
}

func (d *startupDiagnostic) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	text := d.tail + string(p)
	if strings.Contains(text, "AccessDeniedException") {
		d.denied = true
	}
	const limit = 128
	if len(text) > limit {
		text = text[len(text)-limit:]
	}
	d.tail = text
	return len(p), nil
}
func (d *startupDiagnostic) failure() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.denied {
		document := d.document
		if document == "" {
			document = "SSM-SessionManagerRunShell"
		}
		return "Access denied: this role cannot start the SSM shell. Ask your AWS administrator to check ssm:StartSession access for the instance and session document (" + document + ")."
	}
	return "SSM client failed before connecting; see the terminal output for the AWS error."
}

func (d *startupDiagnostic) tunnelFailure() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.denied {
		return "Access denied: the selected profile cannot start this database tunnel. Check ssm:StartSession access to the relay and the AWS-StartPortForwardingSessionToRemoteHost document, or select an authorized profile. Database login was not attempted."
	}
	return ""
}
