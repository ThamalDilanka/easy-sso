//go:build !windows

package process

import (
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

type terminalState struct{ fd, group int }

var terminals sync.Map

func prepare(cmd *exec.Cmd) (func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if f, ok := cmd.Stdin.(*os.File); ok {
		fd := int(f.Fd())
		if group, e := unix.IoctlGetInt(fd, unix.TIOCGPGRP); e == nil {
			// The official interactive client must own the actual terminal's foreground
			// process group, otherwise reading stdin would stop it with SIGTTIN.
			// The helper ignores SIGTTOU so it can restore foreground ownership after
			// the client exits; it never reads or intercepts remote shell input.
			signal.Ignore(syscall.SIGTTOU)
			cmd.SysProcAttr.Foreground = true
			cmd.SysProcAttr.Ctty = fd
			terminals.Store(cmd, terminalState{fd: fd, group: group})
		}
	}
	return func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }, nil
}
func attached(_ *exec.Cmd, stop func() error) (func() error, error) { return stop, nil }
func release(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if value, ok := terminals.LoadAndDelete(cmd); ok {
		s := value.(terminalState)
		_ = unix.IoctlSetPointerInt(s.fd, unix.TIOCSPGRP, s.group)
	}
}
func ownsPID(cmd *exec.Cmd, pid int) bool {
	pg, e := syscall.Getpgid(pid)
	return e == nil && pg == cmd.Process.Pid
}
