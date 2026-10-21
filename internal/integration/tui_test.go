//go:build linux

package integration

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealExecutablePTYUserJourney(t *testing.T) {
	executable := buildExecutable(t)
	h := setup(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	driver := exec.CommandContext(ctx, "python3", filepath.Join(root, "testdata", "tui-journey.py"), executable, h.dir, filepath.Join(h.dir, "aws"))
	if b, e := driver.CombinedOutput(); e != nil {
		t.Fatalf("PTY user journey: %v\n%s", e, b)
	} else {
		t.Log(string(b))
	}
}

// Closing the terminal window sends SIGHUP. The tunnel's plugin, a grandchild
// in its own process group, must not outlive easysso and keep the port.
func TestRealExecutableHangupEndsTunnelPlugin(t *testing.T) {
	executable := buildExecutable(t)
	h := setup(t)
	h.mode(t, map[string]any{"pluginGrandchild": true})
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	driver := exec.CommandContext(ctx, "python3", filepath.Join(root, "testdata", "tui-journey.py"), executable, h.dir, filepath.Join(h.dir, "aws"), "hangup")
	if b, e := driver.CombinedOutput(); e != nil {
		t.Fatalf("PTY hangup journey: %v\n%s", e, b)
	} else {
		t.Log(string(b))
	}
}
