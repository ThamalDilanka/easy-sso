//go:build !windows

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func private(path string, dir bool) error {
	return ValidatePrivate(path)
}

// CheckEnsureDir reports, without changing anything, whether EnsureDir would
// accept path. Unix never repairs a permissive existing directory.
func CheckEnsureDir(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return CheckPath(path)
	}
	return ValidatePrivate(path)
}
func ValidatePrivate(path string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	// Never silently repair permissive pre-existing paths.
	if st.Mode().Perm()&0077 != 0 {
		fix := "chmod 600"
		if st.IsDir() {
			fix = "chmod 700"
		}
		return fmt.Errorf("unsafe permissions on %q (mode %03o); fix with: %s %q", path, st.Mode().Perm(), fix, path)
	}
	if info, ok := st.Sys().(*syscall.Stat_t); !ok || info.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("application storage must be owned by the current user")
	}
	return nil
}
func Protect(path string, dir bool) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if dir {
		mode = 0700
	}
	return os.Chmod(path, mode)
}
func replace(from, to string) error { return os.Rename(from, to) }
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
