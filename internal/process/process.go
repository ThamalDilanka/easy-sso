// Package process owns only processes it starts. It never accepts a recovered PID.
package process

import (
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"os/exec"
	"sync"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	err  error
	stop func() error
}

func Start(cmd *exec.Cmd) (domain.Child, error) {
	stop, err := prepare(cmd)
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		release(cmd)
		return nil, err
	}
	stop, err = attached(cmd, stop)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		release(cmd)
		return nil, err
	}
	c := &child{cmd: cmd, done: make(chan struct{}), stop: stop}
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		release(cmd)
		close(c.done)
	}()
	return c, nil
}
func (c *child) Wait() error { <-c.done; c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *child) Stop() error {
	select {
	case <-c.done:
		return nil
	default:
	}
	return c.stop()
}
func (c *child) PID() int { return c.cmd.Process.Pid }
func (c *child) OwnsPID(pid int) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	return ownsPID(c.cmd, pid)
}
