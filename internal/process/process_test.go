//go:build !windows

package process

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestOwnedProcessCancellationAndWait(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & wait")
	c, e := Start(cmd)
	if e != nil {
		t.Fatal(e)
	}
	if !c.(*child).OwnsPID(cmd.Process.Pid) {
		t.Fatal("started process group not owned")
	}
	if e = c.Stop(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("owned process did not stop")
	}
	if e = c.Stop(); e != nil {
		t.Fatal(e)
	}
}
func TestPTYHelper(t *testing.T) {
	if os.Getenv("PROCESS_PTY_HELPER") != "1" {
		t.Skip("subprocess fixture")
	}
	original, e := unix.IoctlGetInt(0, unix.TIOCGPGRP)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("/bin/sh", "-c", `printf 'READY\n'; read line; printf 'RECEIVED:%s\n' "$line"`)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	c, e := Start(cmd)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Wait(); e != nil {
		t.Fatal(e)
	}
	after, e := unix.IoctlGetInt(0, unix.TIOCGPGRP)
	if e != nil || after != original {
		t.Fatalf("terminal foreground not restored: %d -> %d, %v", original, after, e)
	}
}
func TestInteractiveClientReceivesRealTerminalInput(t *testing.T) {
	if _, e := exec.LookPath("python3"); e != nil {
		t.Skip("python3 PTY test prerequisite")
	}
	script := `import os, pty, subprocess, sys, termios, fcntl, select, time, signal
master, slave = pty.openpty()
def child_setup():
    os.setsid()
    fcntl.ioctl(0, termios.TIOCSCTTY, 0)
env = dict(os.environ, PROCESS_PTY_HELPER='1')
p = subprocess.Popen([sys.argv[1], '-test.run=^TestPTYHelper$'], stdin=slave, stdout=slave, stderr=slave, preexec_fn=child_setup, env=env)
os.close(slave)
output=b''
sent=False
deadline=time.time()+8
while time.time()<deadline:
    if select.select([master], [], [], 0.1)[0]:
        try: data=os.read(master, 4096)
        except OSError: break
        if not data: break
        output+=data
        if b'READY' in output and not sent:
            os.write(master, b'typed-input\n')
            sent=True
    if p.poll() is not None: break
if p.poll() is None:
    os.killpg(p.pid, signal.SIGKILL)
p.wait()
os.close(master)
sys.stdout.buffer.write(output)
if p.returncode != 0 or b'RECEIVED:typed-input' not in output: sys.exit(1)
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, "python3", "-c", script, os.Args[0]).CombinedOutput()
	if e != nil || !strings.Contains(string(b), "RECEIVED:typed-input") {
		t.Fatalf("interactive PTY regression: %v\n%s", e, b)
	}
}
