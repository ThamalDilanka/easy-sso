//go:build linux

package integration

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Call before setup: Go keeps its normal build cache while AWS gets a private home.
func buildExecutable(t *testing.T) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "easysso")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", executable, "./cmd/easysso")
	cmd.Dir = "../.."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build integration executable: %v\n%s", err, output)
	}
	return executable
}
