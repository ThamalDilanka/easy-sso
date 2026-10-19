package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/store"
)

// ErrNoWindowsHome means WSL could not report the Windows profile folder,
// usually because interop is off.
var ErrNoWindowsHome = errors.New("Windows home folder not found")

// ErrNotCABundle means the export destination holds a file that is not a CA
// bundle, so it is kept.
var ErrNotCABundle = errors.New("destination exists and is not a CA bundle")

// Seams: no test runs cmd.exe or wslpath.
var lookPath = exec.LookPath
var runInterop = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if out.Len() > 4096 {
		return nil, errors.New("interop output too long")
	}
	return out.Bytes(), err
}

var windowsHomePattern = regexp.MustCompile(`^[A-Za-z]:\\[^\x00-\x1f%<>:"/|?*]+$`)

// parseWindowsHome reads cmd.exe /u output (UTF-16LE) or plain ASCII.
func parseWindowsHome(out []byte) (string, bool) {
	if len(out) > 4096 {
		return "", false
	}
	text := string(out)
	if bytes.IndexByte(out, 0) >= 0 {
		units := make([]uint16, len(out)/2)
		for i := range units {
			units[i] = uint16(out[2*i]) | uint16(out[2*i+1])<<8
		}
		text = string(utf16.Decode(units))
	}
	text = strings.TrimSpace(text)
	if len(text) >= 260 || !windowsHomePattern.MatchString(text) {
		return "", false
	}
	for _, part := range strings.Split(text, `\`) {
		if part == ".." {
			return "", false
		}
	}
	return text, true
}

var homeCache struct {
	sync.Mutex
	done       bool
	linux, win string
	err        error
}

func resetWindowsHome() {
	homeCache.Lock()
	defer homeCache.Unlock()
	homeCache.done, homeCache.linux, homeCache.win, homeCache.err = false, "", "", nil
}

// windowsHome returns the Windows profile folder as a WSL path and as a
// Windows path. A success lasts for the process; only an explicit request
// retries a failure.
func windowsHome(ctx context.Context, explicit bool) (string, string, error) {
	homeCache.Lock()
	defer homeCache.Unlock()
	if homeCache.done && (homeCache.err == nil || !explicit) {
		return homeCache.linux, homeCache.win, homeCache.err
	}
	homeCache.linux, homeCache.win, homeCache.err = resolveWindowsHome(ctx)
	homeCache.done = true
	return homeCache.linux, homeCache.win, homeCache.err
}

func resolveWindowsHome(ctx context.Context) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd, err := lookPath("cmd.exe")
	if err != nil {
		out, e := runInterop(ctx, "", "wslpath", "-u", `C:\Windows\System32\cmd.exe`)
		cmd = strings.TrimSpace(string(out))
		if st, se := os.Stat(cmd); e != nil || se != nil || !st.Mode().IsRegular() {
			return "", "", ErrNoWindowsHome
		}
	}
	// A Windows working folder avoids cmd.exe's UNC warning; /d skips AutoRun.
	out, err := runInterop(ctx, filepath.Dir(cmd), cmd, "/d", "/u", "/c", "echo", "%USERPROFILE%")
	win, ok := parseWindowsHome(out)
	if err != nil || !ok {
		return "", "", ErrNoWindowsHome
	}
	out, err = runInterop(ctx, "", "wslpath", "-u", win)
	linux := strings.TrimSpace(string(out))
	if err != nil || !filepath.IsAbs(linux) || store.CheckPath(linux) != nil {
		return "", "", ErrNoWindowsHome
	}
	if st, e := os.Stat(linux); e != nil || !st.IsDir() {
		return "", "", ErrNoWindowsHome
	}
	return linux, win, nil
}

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
	linux, win, err := windowsHome(ctx, !refreshOnly)
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
