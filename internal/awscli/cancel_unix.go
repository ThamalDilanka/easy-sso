//go:build !windows

package awscli

import (
	"os"
	"os/exec"
	"syscall"
)

// groupCancel ends the whole process group on context cancel. The official
// session-manager-plugin is a grandchild that holds the port and the output
// pipe; killing only the aws PID would leave it running.
func groupCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		// Never signal a group for a missing process: kill(-0) is our own
		// group and kill(-1) every process the user owns.
		if cmd.Process == nil || cmd.Process.Pid <= 1 {
			return os.ErrProcessDone
		}
		pid := cmd.Process.Pid
		if group, err := syscall.Getpgid(pid); err == nil && group == pid {
			return syscall.Kill(-pid, syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
}
