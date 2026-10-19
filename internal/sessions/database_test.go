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
	if t.mode == "fail" {
		filter([]byte("FATAL: PAM authentication failed for user"))
		close(t.done)
	} else if t.mode != "wait" {
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "PROMPT1=%[") {
				marker := strings.Split(strings.TrimPrefix(arg, "PROMPT1=%["), "%]")[0]
				filter([]byte(marker + "exampledb=> "))
			}
		}
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

func TestDocDBIAMOpenNotYetSupported(t *testing.T) {
	c, role, s, f := fixture(t)
	c.Endpoint = "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com"
	c.DBPort = 27017
	c.AuthMode, c.Database = "docdb-iam", "app_db"
	m := manager(t, f, &bookmarks{}, Options{})
	_, err := m.Open(context.Background(), c, role, s)
	if err == nil || err.Error() != "DocumentDB IAM login is not available in this build" {
		t.Fatalf("Open = %v", err)
	}
	if len(f.starts) != 0 {
		t.Fatalf("transport started: %v", f.starts)
	}
}
