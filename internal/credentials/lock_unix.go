//go:build !windows

package credentials

import (
	"errors"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"os"
	"syscall"
)

func lock(path string) (func(), error) {
	if err := store.CheckPath(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another application operation is in progress")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
