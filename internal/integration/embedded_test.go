//go:build linux

package integration

import (
	"context"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/sessions"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedHelperHandshakeInputAndShutdown(t *testing.T) {
	executable := buildExecutable(t)
	h := setup(t)
	h.activate(t)
	h.mode(t, map[string]any{"terminalEcho": true})
	role, snapshot, err := h.roles.Snapshot("tui-one")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := terminal.NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseAll()
	// A second PTY stands in for an independent native window, without opening a GUI.
	window, err := terminal.NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	defer window.CloseAll()
	manager, err := sessions.NewWithOptions(h.cli, h.state, executable, h.bookmarks, sessions.Options{Terminal: adapter, Window: window, Attempts: 100, Interval: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defer manager.Shutdown(ctx)
	c := domain.Connection{Kind: "ec2", Name: "test", AccountID: role.Profile.AccountID, Partition: "aws", Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}
	run, err := manager.Open(ctx, c, role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "Connected" {
		t.Fatal("helper did not confirm connection")
	}
	if err = adapter.Send(run.ID, "", "hello\n", false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		frame, e := adapter.Frame(run.ID, 90, 20, false)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(frame, "REMOTE:hello") {
			found = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatal("official-client stand-in did not receive PTY input")
	}
	external, err := manager.OpenWindow(ctx, c, role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !window.Has(external.ID) || adapter.Has(external.ID) || window.Has(run.ID) {
		t.Fatal("shell launch ownership crossed adapters")
	}
	if err = manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err = manager.Recheck(ctx, external.ID); err != nil {
		t.Fatalf("external shell did not survive workspace shutdown: %v", err)
	}
	if err = manager.Stop(ctx, external.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range manager.List() {
		if r.ID == run.ID && r.State != "Ended" {
			t.Fatalf("embedded shell left active after shutdown: %s", r.State)
		}
	}
}

// A dropped transport ends the SSM client; the tab's record says so and the
// same frozen identity can open a replacement shell.
func TestEmbeddedShellBrokenPipeThenReconnect(t *testing.T) {
	executable := buildExecutable(t)
	h := setup(t)
	h.activate(t)
	h.mode(t, map[string]any{"brokenPipe": true})
	role, snapshot, err := h.roles.Snapshot("tui-one")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := terminal.NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseAll()
	manager, err := sessions.NewWithOptions(h.cli, h.state, executable, h.bookmarks, sessions.Options{Terminal: adapter, Attempts: 100, Interval: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	defer manager.Shutdown(ctx)
	c := domain.Connection{Kind: "ec2", Name: "test", AccountID: role.Profile.AccountID, Partition: "aws", Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}
	run, err := manager.Open(ctx, c, role, snapshot)
	if err != nil || run.State != "Connected" {
		t.Fatalf("open: %v %+v", err, run)
	}
	// Only a confirmed session loses its transport; the fake waits for this marker.
	if err := os.WriteFile(filepath.Join(h.dir, "broken-pipe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	find := func() domain.Run {
		for _, r := range manager.List() {
			if r.ID == run.ID {
				return r
			}
		}
		t.Fatal("run disappeared")
		return domain.Run{}
	}
	deadline := time.Now().Add(15 * time.Second)
	for !(adapter.Exited(run.ID) && find().State == "Ended") {
		if time.Now().After(deadline) {
			t.Fatalf("ended shell not recorded: exited=%v run=%+v", adapter.Exited(run.ID), find())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ended := find(); !strings.Contains(ended.Error, "official SSM client exited") {
		t.Fatalf("ended record lacks the client exit reason: %+v", ended)
	}
	frame, err := adapter.Frame(run.ID, 120, 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(frame, "\n", ""), "broken pipe") {
		t.Fatalf("plugin message lost from the dead screen: %q", frame)
	}
	h.mode(t, map[string]any{"terminalEcho": true})
	again, err := manager.Reopen(ctx, run.ID)
	if err != nil || again.State != "Connected" || again.ID == run.ID {
		t.Fatalf("reopen: %v %+v", err, again)
	}
	if adapter.Has(run.ID) || !adapter.Has(again.ID) {
		t.Fatal("dead screen kept or replacement missing")
	}
	if err = adapter.Send(again.ID, "", "hello\n", false); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		frame, e := adapter.Frame(again.ID, 90, 20, false)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(frame, "REMOTE:hello") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement shell did not accept input: %q", frame)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if old := find(); old.State != "Ended" {
		t.Fatalf("history record changed: %+v", old)
	}
}
