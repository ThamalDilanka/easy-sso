//go:build !windows

package terminal

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
)

func embeddedSupported() bool { return true }
func startPTY(cmd *exec.Cmd, width, height int) (io.ReadWriteCloser, func(int, int) error, func() error, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)})
	if err != nil {
		return nil, nil, nil, err
	}
	return f, func(w, h int) error { return pty.Setsize(f, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)}) }, cmd.Process.Kill, nil
}
