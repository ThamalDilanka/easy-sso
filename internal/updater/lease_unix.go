//go:build !windows

package updater

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Lease locks the executable inode across all state directories and users. Never
// unlink a lock file: the executable itself stays open until the process exits.
type Lease struct {
	file      *os.File
	path      string
	exclusive bool
	rename    func(string, string) error
}

func Acquire(path string, exclusive bool) (*Lease, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	op := unix.LOCK_SH
	if exclusive {
		op = unix.LOCK_EX
	}
	if err = unix.Flock(int(f.Fd()), op|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another easy-sso app, helper, or updater is active; close it and retry")
	}
	l := &Lease{file: f, path: path, exclusive: exclusive, rename: os.Rename}
	if err = l.current(); err != nil {
		l.Close()
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		l.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() || (exclusive && st.Mode()&(os.ModeSetuid|os.ModeSetgid|0022) != 0) {
		l.Close()
		return nil, errors.New("unsafe executable type or permissions")
	}
	return l, nil
}
func (l *Lease) current() error {
	a, err := l.file.Stat()
	if err != nil {
		return err
	}
	b, err := os.Lstat(l.path)
	if err != nil {
		return err
	}
	if !b.Mode().IsRegular() || !os.SameFile(a, b) {
		return errors.New("executable changed during update; retry using the installed version")
	}
	return nil
}
func (l *Lease) Close() { _ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN); _ = l.file.Close() }
