package launch

import (
	"context"
	"errors"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/wslinterop"
)

// Find lists the installs of app, best effort: the first match per place,
// the local one first. A failed Windows lookup only hides Windows installs.
func (System) Find(ctx context.Context, app App) ([]Install, error) {
	if app != DataGrip && app != Compass {
		return nil, errors.New("unknown database client")
	}
	var found []Install
	switch goos {
	case "darwin":
		if p := firstExec(macCandidates(app)); p != "" {
			found = append(found, Install{app, Local, p})
		}
	case "linux":
		wsl := wslinterop.Detected(goos, getenv)
		if !wsl || getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != "" {
			if p := firstLocal(linuxCandidates(app), wsl); p != "" {
				found = append(found, Install{app, Local, p})
			}
		}
		if wsl {
			if p := findWindows(ctx, app); p != "" {
				found = append(found, Install{app, Windows, p})
			}
		}
	}
	return found, nil
}

func linuxCandidates(app App) []string {
	var names, fixed []string
	if app == DataGrip {
		names = []string{"datagrip", "datagrip.sh"}
		if home := getenv("HOME"); cleanPath(home) {
			fixed = append(fixed, path.Join(home, ".local/share/JetBrains/Toolbox/scripts/datagrip"))
		}
		fixed = append(fixed, "/opt/datagrip/bin/datagrip.sh", "/snap/bin/datagrip")
	} else {
		names = []string{"mongodb-compass"}
		fixed = []string{"/usr/bin/mongodb-compass"}
	}
	var out []string
	for _, n := range names {
		if p, err := lookPath(n); err == nil {
			out = append(out, p)
		}
	}
	return append(out, fixed...)
}

func macCandidates(app App) []string {
	rel := "DataGrip.app/Contents/MacOS/datagrip"
	if app == Compass {
		rel = "MongoDB Compass.app/Contents/MacOS/MongoDB Compass"
	}
	out := []string{path.Join("/Applications", rel)}
	if home := getenv("HOME"); cleanPath(home) {
		out = append(out, path.Join(home, "Applications", rel))
	}
	return out
}

// firstLocal skips Windows files reached through the WSL PATH.
func firstLocal(candidates []string, wsl bool) string {
	var keep []string
	for _, p := range candidates {
		if hasExt(p, ".exe", ".cmd", ".bat") || (wsl && strings.HasPrefix(p, "/mnt/")) {
			continue
		}
		keep = append(keep, p)
	}
	return firstExec(keep)
}

// firstExec returns the first candidate that is an executable regular file.
func firstExec(candidates []string) string {
	for _, p := range candidates {
		if !cleanPath(p) {
			continue
		}
		if st, err := os.Stat(fsRoot + p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// isFile reports whether p is a regular file. Mode bits on /mnt drives say
// nothing about Windows executability.
func isFile(p string) bool {
	st, err := os.Stat(fsRoot + p)
	return err == nil && st.Mode().IsRegular()
}

// windowsDir resolves a Windows folder variable to a checked /mnt path.
func windowsDir(ctx context.Context, name string) string {
	win, err := wslinterop.WindowsEnv(ctx, name)
	if err != nil || win == "" {
		return ""
	}
	p, err := wslinterop.ToLinux(ctx, win)
	if err != nil || !windowsPath(p) {
		return ""
	}
	return p
}

func findWindows(ctx context.Context, app App) string {
	local, programs := windowsDir(ctx, "LOCALAPPDATA"), windowsDir(ctx, "ProgramFiles")
	var candidates []string
	if app == DataGrip {
		if local != "" {
			candidates = append(candidates, path.Join(local, "Programs/DataGrip/bin/datagrip64.exe"))
		}
		if programs != "" {
			candidates = append(candidates, newestDataGrip(path.Join(programs, "JetBrains"))...)
		}
	} else {
		if local != "" {
			candidates = append(candidates, path.Join(local, "MongoDBCompass/MongoDBCompass.exe"))
		}
		if programs != "" {
			candidates = append(candidates, path.Join(programs, "MongoDB Compass/MongoDBCompass.exe"))
		}
	}
	for _, p := range candidates {
		if windowsPath(p) && hasExt(p, ".exe") && isFile(p) {
			return p
		}
	}
	return ""
}

// newestDataGrip lists <dir>/DataGrip*/bin/datagrip64.exe, newest name first.
func newestDataGrip(dir string) []string {
	entries, err := os.ReadDir(fsRoot + dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "DataGrip") {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return newerName(names[i], names[j]) })
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = path.Join(dir, n, "bin/datagrip64.exe")
	}
	return out
}

// newerName compares folder names with digit runs read as numbers, so
// "DataGrip 2025.10" is newer than "DataGrip 2025.9".
func newerName(a, b string) bool {
	for a != "" && b != "" {
		ca, ra := chunk(a)
		cb, rb := chunk(b)
		if ca != cb {
			da, db := isDigit(ca[0]), isDigit(cb[0])
			if da && db {
				na, nb := strings.TrimLeft(ca, "0"), strings.TrimLeft(cb, "0")
				if len(na) != len(nb) {
					return len(na) > len(nb)
				}
				if na != nb {
					return na > nb
				}
			} else {
				return ca > cb
			}
		}
		a, b = ra, rb
	}
	return len(a) > len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// chunk splits off the leading run of digits or non-digits.
func chunk(s string) (string, string) {
	d := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == d {
		i++
	}
	return s[:i], s[i:]
}
