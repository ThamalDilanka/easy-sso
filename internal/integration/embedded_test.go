//go:build linux

package integration

import (
	"context"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/sessions"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
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
