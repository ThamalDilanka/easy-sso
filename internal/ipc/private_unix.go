//go:build !windows

package ipc

import "github.com/ThamalDilanka/easy-sso/internal/store"

func secure(path string, dir bool) error { return store.Protect(path, dir) }
