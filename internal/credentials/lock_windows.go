//go:build windows

package credentials

import (
	"errors"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"syscall"
)

func lock(path string) (func(), error) {
	if err := store.CheckPath(path); err != nil {
		return nil, err
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, errors.New("another application operation is in progress or lock unavailable")
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}
