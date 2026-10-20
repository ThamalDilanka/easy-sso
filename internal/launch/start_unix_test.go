//go:build !windows

package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClient writes an executable script and returns its path.
func fakeClient(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "client")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func startSeams(t *testing.T) **exec.Cmd {
	t.Helper()
	oldGoos, oldGetenv, oldStart, oldWait := goos, getenv, startDetached, handoverWait
	t.Cleanup(func() { goos, getenv, startDetached, handoverWait = oldGoos, oldGetenv, oldStart, oldWait })
	goos, getenv = "linux", fakeEnv{}.get
	var captured *exec.Cmd
	startDetached = func(c *exec.Cmd) error { captured = c; return oldStart(c) }
	return &captured
}

func TestStartDetachedEnvReachesChild(t *testing.T) {
	captured := startSeams(t)
	t.Setenv("AWS_PROFILE", "ambient")
	t.Setenv("AWS_ROLE_ARN", "ambient-role")
	t.Setenv("ELECTRON_RUN_AS_NODE", "1")
	out := filepath.Join(t.TempDir(), "env.txt")
	client := fakeClient(t, `env > "$1.tmp" && mv "$1.tmp" "$1"`)
	err := System{}.Start(context.Background(), Request{Install: Install{Compass, Local, client}, Args: []string{out}, Secret: secretEnv("/dev/null")})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c := *captured
	if c == nil || c.Env != nil {
		t.Fatalf("parent cmd.Env kept after start: %v", c)
	}
	if c.Path != client || len(c.Args) != 2 || c.Args[1] != out {
		t.Fatalf("not started on the binary itself: %q %q", c.Path, c.Args)
	}
	if c.SysProcAttr == nil || !c.SysProcAttr.Setsid {
		t.Fatal("child not in its own session")
	}
	if c.Stdin != nil || c.Stdout != nil || c.Stderr != nil {
		t.Fatal("stdio attached")
	}
	var data []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if data, err = os.ReadFile(out); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("child env not written: %v", err)
	}
	env := envMap(t, strings.Split(strings.TrimSpace(string(data)), "\n"))
	for k, v := range envMap(t, secretEnv("/dev/null")) {
		if env[k] != v {
			t.Errorf("child %s = %q, want %q", k, env[k], v)
		}
	}
	for _, k := range []string{"AWS_PROFILE", "AWS_ROLE_ARN", "ELECTRON_RUN_AS_NODE", "WSLENV"} {
		if _, ok := env[k]; ok {
			t.Errorf("child got %s", k)
		}
	}
}

func TestStartDataGripHandover(t *testing.T) {
	ctx := context.Background()
	startSeams(t)
	handoverWait = 2 * time.Second
	r := Request{Install: Install{DataGrip, Local, fakeClient(t, "exit 0")}, Secret: secretEnv("/dev/null")}
	if err := (System{}).Start(ctx, r); err == nil || err.Error() != "DataGrip handed over to a copy that is already open. Close it, then press g again." {
		t.Fatalf("handover = %v", err)
	}
	r.Install.Path = fakeClient(t, "exit 3")
	if err := (System{}).Start(ctx, r); err == nil || err != ErrClientStopped {
		t.Fatalf("quick failure = %v", err)
	}
	// Compass has no single-instance lock: a quick exit is not a handover.
	r.Install = Install{Compass, Local, r.Install.Path}
	if err := (System{}).Start(ctx, r); err != nil {
		t.Fatalf("Compass quick exit = %v", err)
	}
	handoverWait = 100 * time.Millisecond
	r.Install = Install{DataGrip, Local, fakeClient(t, "sleep 1")}
	if err := (System{}).Start(ctx, r); err != nil {
		t.Fatalf("DataGrip still running = %v", err)
	}
	r.Install.Path = filepath.Join(t.TempDir(), "missing")
	if err := (System{}).Start(ctx, r); err == nil || err != ErrClientStart {
		t.Fatalf("missing binary = %v", err)
	}
}
