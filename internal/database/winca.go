package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"github.com/ThamalDilanka/easy-sso/internal/wslinterop"
)

// ErrNoWindowsHome means WSL could not report the Windows profile folder,
// usually because interop is off.
var ErrNoWindowsHome = wslinterop.ErrNoWindowsHome

// ErrNotCABundle means the export destination holds a file that is not a CA
// bundle, so it is kept.
var ErrNotCABundle = errors.New("destination exists and is not a CA bundle")

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

var cacheName = regexp.MustCompile(`^rds-ca-[a-z-]+\.pem$`)

// exportName keeps the app's own bundle names; a user's file gains a short
// hash of its path so two same-named files never share one export.
func exportName(src string) string {
	name := unsafeName.ReplaceAllString(filepath.Base(src), "_")
	if cacheName.MatchString(name) {
		return name
	}
	abs, _ := filepath.Abs(src)
	sum := sha256.Sum256([]byte(abs))
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".pem" || ext == ".crt" || ext == ".cer" {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	} else {
		ext = ".pem"
	}
	return name + "-" + hex.EncodeToString(sum[:4]) + ext
}

// ExportCAToWindows copies a public CA bundle to %USERPROFILE%\.easy-sso so
// Windows clients can read it. With refreshOnly it only rewrites an earlier
// export, and reports Exists false when there is none.
func ExportCAToWindows(ctx context.Context, src string, refreshOnly bool) (domain.CAExport, error) {
	var exp domain.CAExport
	data, err := readCA(src)
	if err != nil || !validCA(data) {
		return exp, errors.New("RDS CA file unavailable or invalid; select a PEM certificate bundle")
	}
	linux, win, err := wslinterop.Home(ctx, !refreshOnly)
	if err != nil {
		return exp, err
	}
	dir := filepath.Join(linux, ".easy-sso")
	name := exportName(src)
	exp.Windows, exp.Linux = win+`\.easy-sso\`+name, filepath.Join(dir, name)
	if refreshOnly {
		if _, e := os.Lstat(exp.Linux); errors.Is(e, os.ErrNotExist) {
			return domain.CAExport{}, nil
		}
	} else if e := os.Mkdir(dir, 0o755); e != nil && !errors.Is(e, os.ErrExist) {
		return exp, e
	}
	if st, e := os.Lstat(dir); e != nil || !st.IsDir() {
		if e == nil && st.Mode()&os.ModeSymlink != 0 {
			return exp, store.ErrLink
		}
		return exp, errors.New("Windows export folder is not a folder")
	}
	exp.Exists = true
	exp.Changed, err = store.PublicReplace(exp.Linux, data, validCA)
	if errors.Is(err, store.ErrNotReplaceable) {
		err = ErrNotCABundle
	}
	if err != nil {
		exp.Exists, exp.Changed = false, false
	}
	return exp, err
}
