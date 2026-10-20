package launch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/wslinterop"
)

// pgrep reports whether a process matches pattern (macOS). Exit status 1
// means no match.
var pgrep = func(ctx context.Context, pattern string) (bool, error) {
	err := exec.CommandContext(ctx, "pgrep", "-f", pattern).Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// Running reports whether DataGrip is already open where in would start.
// A new DataGrip would hand the request to that copy, which never sees the
// role credentials. Compass has no single-instance lock, so it is never
// checked. A failed check returns ErrRunningUnknown.
func (System) Running(ctx context.Context, in Install) (bool, error) {
	if in.App != DataGrip {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var running bool
	var err error
	switch {
	case in.Place == Windows && goos == "linux" && wslinterop.Detected(goos, getenv):
		running, err = windowsRunning(ctx)
	case in.Place == Local && goos == "linux":
		running, err = procRunning()
	case in.Place == Local && goos == "darwin":
		running, err = pgrep(ctx, "DataGrip.app/Contents/MacOS/datagrip")
	default:
		err = errBadRequest
	}
	if err != nil {
		return false, ErrRunningUnknown
	}
	return running, nil
}

// findTasklist locates tasklist.exe on PATH or through wslpath.
func findTasklist(ctx context.Context) (string, error) {
	if p, err := wslinterop.LookPath("tasklist.exe"); err == nil && windowsPath(p) {
		return p, nil
	}
	p, err := wslinterop.ToLinux(ctx, `C:\Windows\System32\tasklist.exe`)
	if err != nil || !windowsPath(p) || !isFile(p) {
		return "", errors.New("tasklist.exe not found")
	}
	return p, nil
}

func windowsRunning(ctx context.Context) (bool, error) {
	tasklist, err := findTasklist(ctx)
	if err != nil {
		return false, err
	}
	out, err := wslinterop.RunInterop(ctx, path.Dir(tasklist), tasklist, "/FI", "IMAGENAME eq datagrip64.exe", "/FO", "CSV", "/NH")
	if err != nil {
		return false, err
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return false, errors.New("empty tasklist output")
	}
	// A match is a CSV row whose first field is the image name; anything
	// else is the (localized) "no tasks" notice.
	for _, line := range strings.Split(text, "\n") {
		first, _, _ := strings.Cut(strings.TrimSpace(line), ",")
		if strings.EqualFold(strings.Trim(first, `"`), "datagrip64.exe") {
			return true, nil
		}
	}
	return false, nil
}

// procRunning scans /proc for a DataGrip launcher, or the IntelliJ main class
// started from a DataGrip home.
func procRunning() (bool, error) {
	entries, err := os.ReadDir(fsRoot + "/proc")
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		name := e.Name()
		if name == "" || strings.Trim(name, "0123456789") != "" {
			continue
		}
		dir := fsRoot + "/proc/" + name
		comm, _ := os.ReadFile(dir + "/comm")
		if isLauncher(strings.TrimSpace(string(comm))) {
			return true, nil
		}
		cmdline, err := os.ReadFile(dir + "/cmdline")
		if err != nil {
			continue // the process exited, or is not ours to read
		}
		argv := strings.Split(string(bytes.TrimRight(cmdline, "\x00")), "\x00")
		if dataGripArgv(argv) {
			return true, nil
		}
	}
	return false, nil
}

func isLauncher(name string) bool { return name == "datagrip" || name == "datagrip.sh" }

func dataGripArgv(argv []string) bool {
	for i := 0; i < len(argv) && i < 2; i++ {
		if isLauncher(path.Base(argv[i])) {
			return true
		}
	}
	main, home := false, false
	for _, a := range argv {
		if a == "com.intellij.idea.Main" {
			main = true
		}
		if strings.Contains(strings.ToLower(a), "datagrip") {
			home = true
		}
	}
	return main && home
}
