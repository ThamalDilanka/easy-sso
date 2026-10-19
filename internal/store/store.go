// Package store provides private, bounded local persistence. Lock directories
// deliberately fail closed after a crash; operators may remove an abandoned
// lock after verifying there is no running owner.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrLink reports a symbolic link anywhere in a storage path.
var ErrLink = errors.New("unsafe symbolic link in storage path")

func Fingerprint(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func CheckPath(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return ErrLink
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
func EnsureDir(path string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return private(path, true)
}
func Read(path string, limit int64) ([]byte, error) {
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("storage path must be a regular file")
	}
	if st.Size() > limit {
		return nil, errors.New("file exceeds size limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return b, err
}
func AtomicWrite(path string, b []byte) error {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return atomicWrite(path, b)
}

// ExportWrite writes secret-free user-requested artifacts into an existing
// directory without requiring that directory to be private application state.
func ExportWrite(path string, b []byte) error {
	if err := CheckPath(filepath.Dir(path)); err != nil {
		return err
	}
	if err := CheckPath(path); err != nil {
		return err
	}
	// Atomic no-clobber publication: a mistaken destination must never destroy
	// unrelated files, and a concurrent creator must win safely.
	f, err := os.CreateTemp(filepath.Dir(path), ".easy-sso-export-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = private(name, false); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(name, path); err != nil {
		return errors.New("export destination exists or cannot be created atomically; choose a new file path")
	}
	return syncDir(filepath.Dir(path))
}

// ErrNotReplaceable reports an existing file that PublicReplace must keep.
var ErrNotReplaceable = errors.New("destination exists and is not a file this app may replace")

// ErrInUse reports a destination another program holds open.
var ErrInUse = errors.New("destination is in use")

// PublicReplace atomically writes public, secret-free content into a folder
// that need not be private, such as a Windows drive under WSL. An existing
// file is replaced only if replaceable accepts its content; identical
// content is left untouched.
func PublicReplace(path string, b []byte, replaceable func([]byte) bool) (bool, error) {
	if err := CheckPath(filepath.Dir(path)); err != nil {
		return false, err
	}
	if err := CheckPath(path); err != nil {
		return false, err
	}
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() {
			return false, errors.New("storage path must be a regular file")
		}
		old, err := Read(path, 1024*1024)
		if err != nil && !strings.Contains(err.Error(), "size limit") {
			return false, err
		}
		if err == nil && bytes.Equal(old, b) {
			return false, nil
		}
		if err != nil || !replaceable(old) {
			return false, ErrNotReplaceable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".easy-sso-ca-*")
	if err != nil {
		return false, err
	}
	name := f.Name()
	defer os.Remove(name)
	_ = f.Chmod(0644)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err = replace(name, path); err != nil {
		if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EBUSY) {
			return false, ErrInUse
		}
		return false, err
	}
	_ = syncDir(filepath.Dir(path)) // network and drvfs folders may refuse fsync
	return true, nil
}

func atomicWrite(path string, b []byte) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return errors.New("storage path must be a regular file")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".easy-sso-stage-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = private(name, false); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = replace(name, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func Lock(path string) (func(), error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return nil, fmt.Errorf("storage is locked; retry after the other writer finishes (an abandoned lock requires review): %w", err)
	}
	if err := private(path, true); err != nil {
		os.Remove(path)
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}
