//go:build linux

package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/sessions"
	"github.com/ThamalDilanka/easy-sso/internal/terminal"
)

// The bookmark's session document reaches the official client unchanged.
func TestEmbeddedShellUsesSessionDocument(t *testing.T) {
	executable := buildExecutable(t)
	h := setup(t)
	h.activate(t)
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defer manager.Shutdown(ctx)
	c := domain.Connection{Kind: "ec2", Name: "test", AccountID: role.Profile.AccountID, Partition: "aws", Region: "us-east-1", InstanceID: "i-0123456789abcdef0", DocumentName: "Example-ShellDocument"}
	run, err := manager.Open(ctx, c, role, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "Connected" {
		t.Fatalf("helper did not confirm connection: %s %s", run.State, run.Error)
	}
	calls, _ := os.ReadFile(filepath.Join(h.dir, "calls.jsonl"))
	if !bytes.Contains(calls, []byte(`"--document-name"`)) || !bytes.Contains(calls, []byte(`"Example-ShellDocument"`)) {
		t.Fatalf("official client did not receive the session document:\n%s", calls)
	}
}
