//go:build !windows

package awscli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type lockedOutput struct {
	mu sync.Mutex
	b  strings.Builder
}

func (o *lockedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

// alive treats a zombie as gone: it holds no port and no pipe, and only its
// new parent can reap it.
func alive(pid int) bool {
	if stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		return len(fields) > 0 && fields[0] != "Z"
	} else if _, statErr := os.Stat("/proc/self/stat"); statErr == nil {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// The real session-manager-plugin is a grandchild of aws that holds the port
// and the output pipe. A cancelled start must end it too, not only the aws PID.
func TestStartCancelEndsWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "plugin.pid")
	c := fakeClient(t, "sleep 60 &\necho $! > '"+pidFile+".tmp'\nmv '"+pidFile+".tmp' '"+pidFile+"'\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out lockedOutput
	child, err := c.Start(ctx, snapshot(), []string{"ssm", "start-session", "--region", "us-east-1", "--target", "i-0123456789abcdef0"}, nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	pid := 0
	deadline := time.Now().Add(5 * time.Second)
	for pid == 0 {
		if b, e := os.ReadFile(pidFile); e == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if pid == 0 && time.Now().After(deadline) {
			_ = child.Stop()
			t.Fatal("fake plugin did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel left the plugin grandchild holding the session output")
	}
	deadline = time.Now().Add(5 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatal("plugin grandchild outlived the cancelled start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
