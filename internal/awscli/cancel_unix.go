//go:build !windows

package awscli

import (
	"os/exec"
	"syscall"
)

// groupCancel ends the whole process group on context cancel. The official
// session-manager-plugin is a grandchild that holds the port and the output
// pipe; killing only the aws PID would leave it running.
func groupCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		if group, err := syscall.Getpgid(pid); err == nil && group == pid {
			return syscall.Kill(-pid, syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
}
