// Package wslinterop holds the helpers that reach Windows from WSL: running
// cmd.exe and wslpath, and finding the Windows profile folder.
package wslinterop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/ThamalDilanka/easy-sso/internal/store"
)

// ErrNoWindowsHome means WSL could not report the Windows profile folder,
// usually because interop is off.
var ErrNoWindowsHome = errors.New("Windows home folder not found")

// Seams: no test runs cmd.exe or wslpath.
var LookPath = exec.LookPath
var RunInterop = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
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

// Detected reports whether the process runs inside WSL.
func Detected(goos string, getenv func(string) string) bool {
	return goos == "linux" && getenv("WSL_DISTRO_NAME") != ""
}

var windowsHomePattern = regexp.MustCompile(`^[A-Za-z]:\\[^\x00-\x1f%<>:"/|?*]+$`)

// decode reads cmd.exe /u output (UTF-16LE) or plain ASCII.
func decode(out []byte) string {
	if bytes.IndexByte(out, 0) < 0 {
		return string(out)
	}
	units := make([]uint16, len(out)/2)
	for i := range units {
		units[i] = uint16(out[2*i]) | uint16(out[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}

// ParseWindowsHome reads cmd.exe /u output (UTF-16LE) or plain ASCII.
func ParseWindowsHome(out []byte) (string, bool) {
	if len(out) > 4096 {
		return "", false
	}
	text := strings.TrimSpace(decode(out))
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

// ResetHome forgets the cached Windows home folder.
func ResetHome() {
	homeCache.Lock()
	defer homeCache.Unlock()
	homeCache.done, homeCache.linux, homeCache.win, homeCache.err = false, "", "", nil
}

// Home returns the Windows profile folder as a WSL path and as a Windows
// path. A success lasts for the process; only an explicit request retries a
// failure.
func Home(ctx context.Context, explicit bool) (string, string, error) {
	homeCache.Lock()
	defer homeCache.Unlock()
	if homeCache.done && (homeCache.err == nil || !explicit) {
		return homeCache.linux, homeCache.win, homeCache.err
	}
	homeCache.linux, homeCache.win, homeCache.err = resolveHome(ctx)
	homeCache.done = true
	return homeCache.linux, homeCache.win, homeCache.err
}

// findCmd locates cmd.exe, on PATH or through wslpath when the Windows PATH
// is not appended.
func findCmd(ctx context.Context) (string, error) {
	cmd, err := LookPath("cmd.exe")
	if err != nil {
		out, e := RunInterop(ctx, "", "wslpath", "-u", `C:\Windows\System32\cmd.exe`)
		cmd = strings.TrimSpace(string(out))
		if st, se := os.Stat(cmd); e != nil || se != nil || !st.Mode().IsRegular() {
			return "", ErrNoWindowsHome
		}
	}
	return cmd, nil
}

func resolveHome(ctx context.Context) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd, err := findCmd(ctx)
	if err != nil {
		return "", "", err
	}
	// A Windows working folder avoids cmd.exe's UNC warning; /d skips AutoRun.
	out, err := RunInterop(ctx, filepath.Dir(cmd), cmd, "/d", "/u", "/c", "echo", "%USERPROFILE%")
	win, ok := ParseWindowsHome(out)
	if err != nil || !ok {
		return "", "", ErrNoWindowsHome
	}
	out, err = RunInterop(ctx, "", "wslpath", "-u", win)
	linux := strings.TrimSpace(string(out))
	if err != nil || !filepath.IsAbs(linux) || store.CheckPath(linux) != nil {
		return "", "", ErrNoWindowsHome
	}
	if st, e := os.Stat(linux); e != nil || !st.IsDir() {
		return "", "", ErrNoWindowsHome
	}
	return linux, win, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_()]*$`)

// WindowsEnv returns a Windows environment variable, or "" when it is unset.
// The name goes into a cmd.exe command line, so only a plain name is accepted.
func WindowsEnv(ctx context.Context, name string) (string, error) {
	if !envName.MatchString(name) {
		return "", errors.New("invalid Windows variable name")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd, err := findCmd(ctx)
	if err != nil {
		return "", err
	}
	ref := "%" + name + "%"
	out, err := RunInterop(ctx, filepath.Dir(cmd), cmd, "/d", "/u", "/c", "echo", ref)
	if err != nil {
		return "", errors.New("Windows variable lookup failed")
	}
	if len(out) > 4096 {
		return "", errors.New("Windows variable lookup failed")
	}
	val := strings.TrimSpace(decode(out))
	if val == ref {
		return "", nil
	}
	return val, nil
}

func convert(ctx context.Context, flag, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := RunInterop(ctx, "", "wslpath", flag, path)
	res := strings.TrimSpace(string(out))
	if err != nil || res == "" {
		return "", errors.New("path conversion failed")
	}
	return res, nil
}

// ToLinux converts a Windows path to a WSL path.
func ToLinux(ctx context.Context, win string) (string, error) { return convert(ctx, "-u", win) }

// ToWindows converts a WSL path to a Windows path.
func ToWindows(ctx context.Context, linux string) (string, error) {
	return convert(ctx, "-w", linux)
}
