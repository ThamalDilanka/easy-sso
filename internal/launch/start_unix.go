//go:build !windows

package launch

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/wslinterop"
)

// Start runs the client binary itself (no shell, no open, no xdg-open, no
// cmd.exe start) in its own session, so it outlives easysso. The client is
// not owned: it keeps the credentials until it exits.
func (System) Start(ctx context.Context, r Request) error {
	spec, err := build(goos, wslinterop.Detected(goos, getenv), r, os.Environ())
	if err != nil {
		return err
	}
	// exec.Command, not CommandContext: cancelling ctx must not kill the client.
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = startDetached(cmd)
	// Drop our copies of the credentials as soon as the child has them.
	cmd.Env, spec.Env = nil, nil
	if err != nil {
		return ErrClientStart
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if r.Install.App != DataGrip {
		return nil
	}
	// A DataGrip that is already open takes the request and this copy exits
	// at once with status 0, so the new credentials never reach a client.
	timer := time.NewTimer(handoverWait)
	defer timer.Stop()
	select {
	case err := <-done:
		if err == nil {
			return ErrHandedOver
		}
		return ErrClientStopped
	case <-timer.C:
	case <-ctx.Done():
	}
	return nil
}
