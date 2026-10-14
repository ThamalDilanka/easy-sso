// Package terminal launches the existing executable using structured arguments.
// Focus is deliberately unavailable until an adapter has a stable target handle.
package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strings"
	"unicode"
)

type Capabilities struct{ LaunchTab, LaunchWindow, FocusExisting, FocusWorkspace, SetTitle, StableHandles bool }
type Request struct{ Executable, StateDir, RunID, Title string }
type Adapter interface {
	Launch(context.Context, Request) (string, error)
	Focus(string) error
	Capabilities() Capabilities
}
type Native struct{ Kind, Path, Distro, User string }

func Detect() (Adapter, error) {
	if runtime.GOOS == "windows" {
		p, e := exec.LookPath("wt.exe")
		if e != nil {
			return nil, errors.New("Windows Terminal is unavailable; install it before opening shells")
		}
		return &Native{Kind: "windows", Path: p}, nil
	}
	if runtime.GOOS == "darwin" {
		return &Native{Kind: "macos", Path: "/usr/bin/osascript"}, nil
	}
	data, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	if strings.Contains(strings.ToLower(string(data)), "microsoft") {
		distro := os.Getenv("WSL_DISTRO_NAME")
		u, e := user.Current()
		if distro == "" || e != nil {
			return nil, errors.New("WSL terminal launch requires a pinned distribution and current user")
		}
		p, e := exec.LookPath("wt.exe")
		if e != nil {
			return nil, errors.New("Windows Terminal interop is unavailable; shell launch unsupported")
		}
		return &Native{Kind: "wsl", Path: p, Distro: distro, User: u.Username}, nil
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, errors.New("native shell launch is unavailable on this headless host")
	}
	p, e := exec.LookPath("gnome-terminal")
	if e != nil {
		return nil, errors.New("GNOME Terminal is unavailable; install it before opening shells")
	}
	return &Native{Kind: "gnome", Path: p}, nil
}
func CleanTitle(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
}
func (n *Native) Capabilities() Capabilities {
	return Capabilities{LaunchWindow: true, SetTitle: n.Kind != "macos"}
}
func (n *Native) Focus(string) error {
	return errors.New("terminal focus unavailable; switch using terminal tabs")
}
func (n *Native) Arguments(r Request) ([]string, error) {
	if r.Executable == "" || r.StateDir == "" || !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(r.RunID) {
		return nil, errors.New("invalid helper launch parameters")
	}
	args := []string{r.Executable, "internal-helper", "--state-dir", r.StateDir, "--run-id", r.RunID}
	title := CleanTitle(r.Title)
	switch n.Kind {
	case "gnome":
		return append([]string{"--window", "--title", title, "--"}, args...), nil
	case "windows":
		return append([]string{"-w", "new", "new-tab", "--title", title, "--"}, args...), nil
	case "wsl":
		if n.Distro == "" || n.User == "" {
			return nil, errors.New("WSL distribution and user must be pinned")
		}
		return append([]string{"-w", "new", "new-tab", "--title", title, "wsl.exe", "--distribution", n.Distro, "--user", n.User, "--exec"}, args...), nil
	case "macos":
		return append([]string{"-e", `on run argv
set commandText to ""
repeat with itemText in argv
set commandText to commandText & quoted form of (itemText as text) & " "
end repeat
tell application "Terminal"
activate
do script commandText
end tell
end run`, "--"}, args...), nil
	default:
		return nil, fmt.Errorf("unsupported terminal adapter %q", n.Kind)
	}
}
func (n *Native) Launch(ctx context.Context, r Request) (string, error) {
	args, e := n.Arguments(r)
	if e != nil {
		return "", e
	}
	cmd := exec.CommandContext(ctx, n.Path, args...)
	if e = cmd.Run(); e != nil {
		return "", errors.New("native terminal launch failed; check the configured terminal")
	}
	return r.RunID, nil
}
