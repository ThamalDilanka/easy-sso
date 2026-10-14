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
