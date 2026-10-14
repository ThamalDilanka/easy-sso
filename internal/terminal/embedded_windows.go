//go:build windows

package terminal

import (
	"errors"
	"io"
	"os/exec"
)

func embeddedSupported() bool { return false }
func startPTY(*exec.Cmd, int, int) (io.ReadWriteCloser, func(int, int) error, func() error, error) {
	return nil, nil, nil, errors.New("run easysso in WSL for embedded terminals")
}
