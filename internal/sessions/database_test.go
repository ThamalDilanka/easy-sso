package sessions

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/database"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/ipc"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
)

type signingCLI struct {
	*fakeCLI
	tokenSnapshot domain.Snapshot
	tokenCalls    int
	tokenErr      bool
}

func (f *signingCLI) DatabaseToken(_ context.Context, s domain.Snapshot, _ domain.Connection) (string, error) {
	f.tokenSnapshot = s
	f.tokenCalls++
	if f.tokenErr {
		return "", errors.New("SECRET")
	}
	return "SECRET-IAM-TOKEN", nil
}

type clientTerminal struct {
	mu          sync.Mutex
	done        chan struct{}
	exists      bool
	mode        string
	launchCount int
	env         []string
	filter      func([]byte) []byte
	marker      string
}

func (t *clientTerminal) Launch(context.Context, terminal.Request) (string, error) {
	return "", errors.New("unexpected helper")
}
func (t *clientTerminal) Capabilities() terminal.Capabilities { return terminal.Capabilities{} }
func (t *clientTerminal) Focus(string) error                  { return nil }
func (t *clientTerminal) Has(string) bool                     { t.mu.Lock(); defer t.mu.Unlock(); return t.exists }
func (t *clientTerminal) Exited(string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done == nil {
		return false
	}
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}
func (t *clientTerminal) Close(string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done != nil {
		select {
		case <-t.done:
		default:
			close(t.done)
		}
	}
	t.exists = false
	return nil
}
func (t *clientTerminal) LaunchClient(_ context.Context, _ string, cmd *exec.Cmd, filter func([]byte) []byte) (<-chan struct{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.launchCount++
	t.exists = true
	t.done = make(chan struct{})
	t.env = append([]string(nil), cmd.Env...)
	t.filter, t.marker = filter, ""
	for i, arg := range cmd.Args {
		if strings.HasPrefix(arg, "PROMPT1=%[") {
			t.marker = strings.Split(strings.TrimPrefix(arg, "PROMPT1=%["), "%]")[0]
		}
		// mongosh sets its prompt in the --eval script.
		if arg == "--eval" && i+1 < len(cmd.Args) && strings.Contains(cmd.Args[i+1], "prompt = () => '") {
			t.marker = strings.Split(strings.SplitN(cmd.Args[i+1], "prompt = () => '", 2)[1], "'")[0]
		}
	}
	if t.mode == "fail" {
		filter([]byte("FATAL: PAM authentication failed for user"))
		close(t.done)
	} else if t.mode != "wait" && t.marker != "" {
		filter([]byte(t.marker + "exampledb=> "))
	}
	return t.done, nil
}

func dbManager(t *testing.T, mode string) (*Manager, domain.Connection, domain.ManagedRole, domain.Snapshot, *signingCLI, *clientTerminal, *bookmarks) {
	t.Helper()
	c, role, s, f := fixture(t)
	c.AuthMode = "postgres-iam"
	c.Database = "exampledb"
	c.DBUser = "iam_reader"
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := server.Certificate()
	server.Close()
	dir := t.TempDir()
	c.CAFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(c.CAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "psql"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cli := &signingCLI{fakeCLI: f}
	term := &clientTerminal{mode: mode}
	book := &bookmarks{}
	m, err := NewWithOptions(cli, t.TempDir(), "unused", book, Options{Terminal: term, Attempts: 2, Interval: time.Millisecond, Probe: func(int, domain.Child) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m, c, role, s, cli, term, book
}

func TestDatabaseOwnedClientLifecycleAndReuse(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing-tunnel"}[reuse], func(t *testing.T) {
			m, c, role, s, cli, term, book := dbManager(t, "")
			if reuse {
				transport := c
				transport.AuthMode = ""
				if _, err := m.Open(context.Background(), transport, role, s); err != nil {
					t.Fatal(err)
				}
			}
			run, err := m.Open(context.Background(), c, role, s)
			if err != nil || run.State != "Connected" {
				t.Fatal(run.State, err)
			}
			if cli.tokenSnapshot != s || cli.tokenCalls != 1 {
				t.Fatal("signer did not receive frozen credentials")
			}
			again, err := m.Open(context.Background(), c, role, s)
			if err != nil || again.ID != run.ID || term.launchCount != 1 || len(cli.starts) != 1 {
				t.Fatal("duplicate connection created", err)
			}
			changed := c
			changed.DBUser = "other"
			if _, err = m.Open(context.Background(), changed, role, s); err == nil {
				t.Fatal("shared client identity changed")
			}
			if err = m.CloseShell(context.Background(), run.ID); err != nil {
				t.Fatal(err)
			}
			if term.Has(run.ID) {
				t.Fatal("closed client retained")
			}
			if reuse && (len(cli.terminations) != 0 || m.List()[0].State != "Listening") {
				t.Fatal("preexisting tunnel stopped")
			}
			if !reuse && len(cli.terminations) != 1 {
				t.Fatal("new tunnel not cleaned")
			}
			saved, _ := book.List()
			if len(saved) == 0 {
				t.Fatal("authenticated bookmark not saved")
			}
			files, _ := filepath.Glob(filepath.Join(m.stateDir, "runtime", "*.json"))
			for _, path := range files {
				data, _ := os.ReadFile(path)
				if strings.Contains(string(data), "SECRET") {
					t.Fatal("secret persisted")
				}
			}
		})
	}
}

func TestDatabaseFailedLoginCleanup(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing-tunnel"}[reuse], func(t *testing.T) {
			m, c, role, s, cli, term, book := dbManager(t, "fail")
			if reuse {
				transport := c
				transport.AuthMode = ""
				if _, err := m.Open(context.Background(), transport, role, s); err != nil {
					t.Fatal(err)
				}
			}
			run, err := m.Open(context.Background(), c, role, s)
			if err == nil || !strings.Contains(err.Error(), "rds-db:connect") {
				t.Fatal("missing IAM setup diagnosis", err)
			}
			if run.State == "Connected" {
				t.Fatal("failed login marked connected")
			}
			saved, _ := book.List()
			want := 0
			if reuse {
				want = 1
			}
			if len(saved) != want {
				t.Fatal("failed login saved bookmark")
			}
			if !term.Has(run.ID) {
				t.Fatal("failed terminal output lost")
			}
			if reuse && len(cli.terminations) != 0 {
				t.Fatal("preexisting tunnel stopped")
			}
			if !reuse && len(cli.terminations) != 1 {
				t.Fatal("failed startup leaked new tunnel")
			}
		})
	}
}

func TestDatabaseMissingInputsAndCredentialsDoNotStartTransport(t *testing.T) {
	m, c, role, s, cli, _, _ := dbManager(t, "")
	c.DBUser = ""
	if _, err := m.Open(context.Background(), c, role, s); err == nil {
		t.Fatal("missing user accepted")
	}
	c.DBUser = "reader"
	c.CAFile = "/nonexistent/ca.pem"
	if _, err := m.Open(context.Background(), c, role, s); err == nil {
		t.Fatal("missing CA accepted")
	}
	if len(cli.starts) != 0 || cli.tokenCalls != 0 {
		t.Fatal("invalid prerequisite started tunnel or signer")
	}
}

func TestDatabaseCancellationAndSigningFailureCleanup(t *testing.T) {
	for _, mode := range []string{"cancel", "sign"} {
		t.Run(mode, func(t *testing.T) {
			m, c, role, s, cli, term, book := dbManager(t, "wait")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "sign" {
				cli.tokenErr = true
			} else {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			run, err := m.Open(ctx, c, role, s)
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe failure", err)
			}
			if len(cli.terminations) != 1 || term.Has(run.ID) {
				t.Fatal("failed launch leaked client or tunnel")
			}
			saved, _ := book.List()
			if len(saved) > 0 {
				t.Fatal("failed launch saved bookmark")
			}
		})
	}
}

func TestDatabaseNaturalExitStopAndShutdown(t *testing.T) {
	for _, action := range []string{"exit", "stop", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			m, c, role, s, cli, term, _ := dbManager(t, "")
			run, err := m.Open(context.Background(), c, role, s)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.Recheck(context.Background(), run.ID); err != nil || m.List()[0].State != "Connected" {
				t.Fatal("transport recheck lost authenticated state", err)
			}
			switch action {
			case "exit":
				term.mu.Lock()
				close(term.done)
				term.mu.Unlock()
				deadline := time.Now().Add(time.Second)
				for m.List()[0].State != "Ended" && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if m.List()[0].State != "Ended" {
					t.Fatal("client exit did not clean tunnel")
				}
				if !term.Has(run.ID) {
					t.Fatal("natural exit hid terminal output")
				}
				if err = m.CloseShell(context.Background(), run.ID); err != nil {
					t.Fatal(err)
				}
			case "stop":
				if err = m.Stop(context.Background(), run.ID); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				if err = m.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			cli.mu.Lock()
			count := len(cli.terminations)
			cli.mu.Unlock()
			if count != 1 || term.Has(run.ID) {
				t.Fatal("client or tunnel survived cleanup", action, count)
			}
		})
	}
}

func TestDatabaseReuseRejectsChangedIdentityOrRoute(t *testing.T) {
	for _, change := range []string{"snapshot", "route"} {
		t.Run(change, func(t *testing.T) {
			m, c, role, s, cli, _, _ := dbManager(t, "")
			transport := c
			transport.AuthMode = ""
			_, err := m.Open(context.Background(), transport, role, s)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "snapshot":
				s.SessionToken = "new-session"
			case "route":
				c.Endpoint = "other.example.rds.amazonaws.com"
			}
			if _, err = m.Open(context.Background(), c, role, s); err == nil {
				t.Fatal("unsafe tunnel reused")
			}
			if cli.tokenCalls != 0 || len(cli.starts) != 1 || len(cli.terminations) != 0 {
				t.Fatal("unsafe reuse affected process or signed credentials")
			}

		})
	}
}

func TestDatabaseMissingPSQLDoesNotOpenTunnel(t *testing.T) {
	m, c, role, s, cli, _, _ := dbManager(t, "")
	t.Setenv("PATH", t.TempDir())
	if _, err := m.Open(context.Background(), c, role, s); err == nil || !strings.Contains(err.Error(), "install psql") {
		t.Fatal("missing prerequisite diagnosis", err)
	}
	if len(cli.starts) != 0 || cli.tokenCalls != 0 {
		t.Fatal("missing client started transport")
	}
}

func TestRecoveredDatabaseRecordDoesNotReserveFreePort(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "free", true: "external-listener"}[occupied], func(t *testing.T) {
			m, c, role, s, cli, _, _ := dbManager(t, "")
			old := domain.Run{ID: runID(), Connection: c, Profile: role.Profile, Alias: role.Alias, Identity: role.Identity, Expiration: s.Expiration, State: "Listening", SessionID: "old-remote-session", StartedAt: time.Now()}
			if err := ipc.WriteJSON(filepath.Join(m.stateDir, "runtime", old.ID+".json"), registration{Run: old}); err != nil {
				t.Fatal(err)
			}
			m.recover()
			if occupied {
				l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(c.LocalPort)))
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
			}
			run, err := m.Open(context.Background(), c, role, s)
			if occupied {
				if err == nil || len(cli.starts) != 0 || cli.tokenCalls != 0 {
					t.Fatal("adopted external listener", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("stale record blocked a free port: %v", err)
			}
			if run.ID == old.ID || run.State != "Connected" || len(cli.starts) != 1 {
				t.Fatal("did not start an independently owned connection")
			}
			if len(cli.terminations) != 0 {
				t.Fatal("terminated recovered remote session")
			}
		})
	}
}

func TestDatabaseAuthorizationFailureExplainsSSMPermission(t *testing.T) {
	for _, iam := range []bool{false, true} {
		t.Run(fmt.Sprint(iam), func(t *testing.T) {
			m, c, role, s, _, _, book := dbManager(t, "")
			if !iam {
				c.AuthMode = ""
			}
			m.cli = deniedDatabaseCLI{deniedCLI{fakeCLI: &fakeCLI{identity: role.Identity}}}
			m.options.Attempts = 20
			m.options.Interval = time.Millisecond
			run, err := m.Open(context.Background(), c, role, s)
			if err == nil || !strings.Contains(err.Error(), "Access denied") || !strings.Contains(err.Error(), "AWS-StartPortForwardingSessionToRemoteHost") {
				t.Fatalf("authorization diagnosis lost: %v", err)
			}
			if run.State != "Failed" || !strings.Contains(run.Error, "Access denied") {
				t.Fatalf("failed state not actionable: %s %s", run.State, run.Error)
			}
			saved, _ := book.List()
			if len(saved) != 0 {
				t.Fatal("authorization failure saved bookmark")
			}
			data, err := os.ReadFile(filepath.Join(m.stateDir, "runtime", run.ID+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "Access denied") || strings.Contains(string(data), "SECRET") {
				t.Fatal("persisted failure missing or unsafe")
			}
		})
	}
}

type deniedDatabaseCLI struct{ deniedCLI }

func (deniedDatabaseCLI) DatabaseToken(context.Context, domain.Snapshot, domain.Connection) (string, error) {
	return "", errors.New("token signing must not occur after SSM denial")
}

// docdbManager is dbManager for a docdb-iam bookmark, with a fake mongosh on
// PATH and the TLS check stubbed (the fake tunnel has no listener). Without a
// signer it holds a plain fakeCLI, so any DatabaseToken call would fail.
func docdbManager(t *testing.T, mode string, signer bool) (*Manager, domain.Connection, domain.ManagedRole, domain.Snapshot, *fakeCLI, *clientTerminal, *bookmarks) {
	t.Helper()
	m, c, role, s, cli, term, book := dbManager(t, mode)
	plain := cli.fakeCLI
	if !signer {
		_ = m.Shutdown(context.Background())
		var err error
		m, err = NewWithOptions(plain, t.TempDir(), "unused", book, Options{Terminal: term, Attempts: 2, Interval: time.Millisecond, Probe: func(int, domain.Child) (bool, error) { return true, nil }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	}
	m.verifyTLS = func(context.Context, int, string, string) error { return nil }
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "mongosh"), []byte("#!/bin/sh\necho 2.9.2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c.Endpoint, c.DBPort = "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com", 27017
	c.AuthMode, c.Database, c.DBUser = "docdb-iam", "app_db", ""
	return m, c, role, s, plain, term, book
}

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v, true
		}
	}
	return "", false
}

// mongoHomes lists the private mongosh homes left for run id.
func mongoHomes(m *Manager, id string) []string {
	homes, _ := filepath.Glob(filepath.Join(m.stateDir, "mongosh", id+"-*"))
	return homes
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDocDBIAMOwnedClientLifecycle(t *testing.T) {
	t.Setenv("AWS_PROFILE", "ambient-profile")
	m, c, role, s, cli, term, book := docdbManager(t, "wait", false)
	type result struct {
		run domain.Run
		err error
	}
	opened := make(chan result, 1)
	go func() {
		run, err := m.Open(context.Background(), c, role, s)
		opened <- result{run, err}
	}()
	waitFor(t, "mongosh not launched", func() bool { term.mu.Lock(); defer term.mu.Unlock(); return term.launchCount == 1 })
	if saved, _ := book.List(); len(saved) != 0 {
		t.Fatal("bookmark saved before the authenticated prompt")
	}
	term.mu.Lock()
	env, filter, marker := term.env, term.filter, term.marker
	term.mu.Unlock()
	for key, want := range map[string]string{"AWS_ACCESS_KEY_ID": s.AccessKeyID, "AWS_SECRET_ACCESS_KEY": s.SecretAccessKey, "AWS_SESSION_TOKEN": s.SessionToken} {
		if got, ok := envValue(env, key); !ok || got != want {
			t.Fatalf("%s not the frozen snapshot value", key)
		}
	}
	if _, ok := envValue(env, "AWS_PROFILE"); ok {
		t.Fatal("ambient AWS_PROFILE reached mongosh")
	}
	home, _ := envValue(env, "HOME")
	if homes := mongoHomes(m, m.List()[0].ID); len(homes) != 1 || homes[0] != home {
		t.Fatalf("private home %q not in %v", home, homes)
	}
	if marker == "" {
		t.Fatal("no prompt marker in the mongosh arguments")
	}
	filter([]byte(marker + "app_db> "))
	got := <-opened
	if got.err != nil || got.run.State != "Connected" {
		t.Fatal(got.run.State, got.err)
	}
	if saved, _ := book.List(); len(saved) != 1 || saved[0].AuthMode != "docdb-iam" {
		t.Fatal("authenticated bookmark not saved", saved)
	}
	if len(cli.starts) != 1 {
		t.Fatal("tunnel not started once", cli.starts)
	}
	term.mu.Lock()
	close(term.done)
	term.mu.Unlock()
	waitFor(t, "client exit did not stop the owned tunnel", func() bool { return m.List()[0].State == "Ended" })
	cli.mu.Lock()
	stopped := len(cli.terminations)
	cli.mu.Unlock()
	if stopped != 1 {
		t.Fatal("owned tunnel not terminated", stopped)
	}
	waitFor(t, "private home kept after exit", func() bool { return len(mongoHomes(m, got.run.ID)) == 0 })
}

func TestDocDBIAMUsesFrozenSnapshot(t *testing.T) {
	m, c, role, s, cli, term, _ := docdbManager(t, "", false)
	renewed := s
	renewed.AccessKeyID, renewed.SecretAccessKey, renewed.SessionToken = "ASIARENEWED", "RENEWED-SECRET", "RENEWED-TOKEN"
	renewed.Expiration = s.Expiration.Add(time.Hour)
	renewedRole := role
	renewedRole.Expiration = renewed.Expiration
	m.options.RecoveryCredentials = func(string) (domain.ManagedRole, domain.Snapshot, error) { return renewedRole, renewed, nil }
	transport := c
	transport.AuthMode, transport.Database = "", ""
	tunnel, err := m.Open(context.Background(), transport, role, s)
	if err != nil {
		t.Fatal(err)
	}
	frozen := func(where string) {
		t.Helper()
		term.mu.Lock()
		env := term.env
		term.mu.Unlock()
		if got, _ := envValue(env, "AWS_ACCESS_KEY_ID"); got != s.AccessKeyID {
			t.Fatalf("%s: child key %q is not the run's frozen key", where, got)
		}
		for _, kv := range env {
			if strings.Contains(kv, "RENEWED") {
				t.Fatalf("%s: renewed credentials reached mongosh", where)
			}
		}
	}
	run, err := m.Open(context.Background(), c, role, s)
	if err != nil || run.ID != tunnel.ID || run.State != "Connected" {
		t.Fatal("client did not attach to the tunnel", run.State, err)
	}
	frozen("first client")
	if err = m.CloseShell(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Open(context.Background(), c, renewedRole, renewed); err == nil {
		t.Fatal("renewed role attached to a tunnel frozen with other credentials")
	}
	if term.launchCount != 1 || len(cli.starts) != 1 {
		t.Fatal("renewed role launched a client or tunnel", term.launchCount, len(cli.starts))
	}
	if _, err = m.Open(context.Background(), c, role, s); err != nil {
		t.Fatal(err)
	}
	frozen("second client")
}

func TestDocDBIAMRefusedWithoutEmbeddedTerminal(t *testing.T) {
	_, c, role, s, cli, _, _ := docdbManager(t, "", false)
	m := manager(t, cli, &bookmarks{}, Options{Terminal: &helperTerminal{}})
	_, err := m.Open(context.Background(), c, role, s)
	if err == nil || err.Error() != "DocumentDB IAM login needs Linux, macOS or WSL" {
		t.Fatalf("Open = %v", err)
	}
	if len(cli.starts) != 0 {
		t.Fatal("transport started", cli.starts)
	}
}

func TestDocDBIAMReuseNeedsSameAuthMode(t *testing.T) {
	t.Run("postgres-iam-client", func(t *testing.T) {
		m, docdb, role, s, cli, term, _ := docdbManager(t, "", true)
		pg := docdb
		pg.Endpoint, pg.DBPort, pg.AuthMode, pg.Database, pg.DBUser = "app.example.rds.amazonaws.com", 5432, "postgres-iam", "exampledb", "iam_reader"
		if _, err := m.Open(context.Background(), pg, role, s); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Open(context.Background(), docdb, role, s); err == nil {
			t.Fatal("docdb-iam attached to a PostgreSQL client's port")
		}
		if term.launchCount != 1 || len(cli.starts) != 1 {
			t.Fatal("refused open launched something", term.launchCount, len(cli.starts))
		}
	})
	t.Run("tunnel-only", func(t *testing.T) {
		m, c, role, s, cli, term, _ := docdbManager(t, "", false)
		transport := c
		transport.AuthMode, transport.Database = "", ""
		tunnel, err := m.Open(context.Background(), transport, role, s)
		if err != nil {
			t.Fatal(err)
		}
		run, err := m.Open(context.Background(), c, role, s)
		if err != nil || run.ID != tunnel.ID || run.State != "Connected" || term.launchCount != 1 || len(cli.starts) != 1 {
			t.Fatal("docdb-iam did not attach to the tunnel-only run", run.State, err)
		}
	})
	t.Run("other-client-mode", func(t *testing.T) {
		m, c, role, s, cli, term, _ := docdbManager(t, "", false)
		run, err := m.Open(context.Background(), c, role, s)
		if err != nil {
			t.Fatal(err)
		}
		if again, err := m.Open(context.Background(), c, role, s); err != nil || again.ID != run.ID || term.launchCount != 1 {
			t.Fatal("same client not reused", err)
		}
		// The running client logs in another way: never hand it out as this one.
		en, _ := m.find(run.ID)
		en.mu.Lock()
		en.run.Connection.AuthMode = "postgres-iam"
		en.mu.Unlock()
		if _, err := m.Open(context.Background(), c, role, s); err == nil {
			t.Fatal("client with another auth mode reused")
		}
		other := c
		other.Database = "other_db"
		en.mu.Lock()
		en.run.Connection.AuthMode = "docdb-iam"
		en.mu.Unlock()
		if _, err := m.Open(context.Background(), other, role, s); err == nil {
			t.Fatal("client for another database reused")
		}
		if term.launchCount != 1 || len(cli.starts) != 1 {
			t.Fatal("refused open launched something", term.launchCount, len(cli.starts))
		}
	})
}

func TestDocDBIAMHomeRemovedOnFailure(t *testing.T) {
	for _, mode := range []string{"fail", "timeout", "tls"} {
		t.Run(mode, func(t *testing.T) {
			termMode := mode
			if mode != "fail" {
				termMode = "wait"
			}
			m, c, role, s, cli, term, book := docdbManager(t, termMode, false)
			ctx := context.Background()
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			if mode == "tls" {
				// The real check: nothing listens on the fake tunnel's port.
				m.verifyTLS = database.VerifyTLS
			}
			run, err := m.Open(ctx, c, role, s)
			if err == nil || run.State == "Connected" {
				t.Fatal("failed login reported success", run.State)
			}
			for _, secret := range []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken} {
				if strings.Contains(err.Error(), secret) || strings.Contains(run.Error, secret) {
					t.Fatal("failure text carries a secret")
				}
			}
			switch mode {
			case "fail":
				if !strings.Contains(err.Error(), "$external") || !term.Has(run.ID) {
					t.Fatal("login failure not explained or output lost", err)
				}
			case "tls":
				if !strings.Contains(err.Error(), "The tunnel did not answer") || term.launchCount != 0 {
					t.Fatal("TLS failure not caught before launch", err)
				}
			}
			cli.mu.Lock()
			stopped := len(cli.terminations)
			cli.mu.Unlock()
			if stopped != 1 {
				t.Fatal("failed startup leaked the new tunnel", stopped)
			}
			if saved, _ := book.List(); len(saved) != 0 {
				t.Fatal("failed login saved bookmark")
			}
			waitFor(t, "private home kept after failure", func() bool { return len(mongoHomes(m, run.ID)) == 0 })
		})
	}
}

func TestDocDBIAMRefusesRunWithoutOwnCredentials(t *testing.T) {
	m, c, role, s, _, term, _ := docdbManager(t, "", false)
	for _, recovered := range []bool{true, false} {
		en := &entry{run: domain.Run{ID: runID(), Connection: c, Profile: role.Profile, Alias: role.Alias, Identity: role.Identity, State: "Listening"}, keepTunnel: true, recovered: recovered}
		if recovered {
			en.snapshot = s
		}
		if err := m.openDatabaseClient(context.Background(), en, term, "mongosh", c.CAFile); err == nil {
			t.Fatal("client started without the run's own credentials", recovered)
		}
	}
	if term.launchCount != 0 {
		t.Fatal("mongosh launched", term.launchCount)
	}
}

func TestStartupSweepsOrphanMongoHomes(t *testing.T) {
	c, role, s, f := fixture(t)
	dir := t.TempDir()
	live := domain.Run{ID: runID(), Connection: c, Profile: role.Profile, Alias: role.Alias, Identity: role.Identity, Expiration: s.Expiration, State: "Connected", SessionID: "remote-session", StartedAt: time.Now()}
	if err := ipc.PrivateDir(filepath.Join(dir, "runtime")); err != nil {
		t.Fatal(err)
	}
	if err := ipc.WriteJSON(filepath.Join(dir, "runtime", live.ID+".json"), registration{Run: live}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	homes := map[string]bool{live.ID + "-kept": true, runID() + "-orphan": false}
	for name := range homes {
		path := filepath.Join(dir, "mongosh", name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "mongosh"), 0700); err != nil {
		t.Fatal(err)
	}
	m, err := NewWithOptions(f, dir, "unused", &bookmarks{}, Options{Terminal: &clientTerminal{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	for name, kept := range homes {
		if _, err := os.Stat(filepath.Join(dir, "mongosh", name)); (err == nil) != kept {
			t.Fatalf("%s: kept=%v, want %v", name, err == nil, kept)
		}
	}
}
