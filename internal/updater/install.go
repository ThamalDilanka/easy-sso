package updater

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/releases"
)

type Installation struct{ Path, Channel string }

// Inspect resolves launcher symlinks before checking manager locations and
// installation metadata. Release linker flags alone never authorize replacement.
func Inspect(path, distribution string) (Installation, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Installation{}, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return Installation{}, err
	}
	result := Installation{Path: resolved, Channel: "unknown"}
	normalized := "/" + strings.Trim(strings.ToLower(filepath.ToSlash(resolved)), "/") + "/"
	switch {
	case strings.Contains(normalized, "/cellar/easy-sso/"):
		result.Channel = "homebrew"
	case strings.Contains(normalized, "/scoop/apps/easy-sso/"):
		result.Channel = "scoop"
	case strings.Contains(normalized, "/microsoft/winget/packages/"):
		result.Channel = "winget"
	}
	if result.Channel != "unknown" {
		return result, nil
	}
	if distribution == "go" {
		result.Channel = "go"
		return result, nil
	}
	if distribution != "release" || filepath.Base(resolved) != "easysso" && filepath.Base(resolved) != "easysso.exe" {
		return result, nil
	}
	marker := filepath.Join(filepath.Dir(resolved), "easy-sso.install.json")
	st, err := os.Lstat(marker)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4096 {
		return result, nil
	}
	f, err := os.Open(marker)
	if err != nil {
		return result, nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return result, nil
	}
	var meta struct {
		Channel    string `json:"channel"`
		Repository string `json:"repository"`
	}
	if json.Unmarshal(b, &meta) != nil || meta.Repository != releases.Contract.Repository {
		return result, nil
	}
	switch meta.Channel {
	case "direct", "homebrew", "scoop", "winget":
		result.Channel = meta.Channel
	}
	return result, nil
}
func (i Installation) Guidance(version string) string {
	switch i.Channel {
	case "homebrew":
		return "Upgrade with: brew upgrade easy-sso"
	case "scoop":
		return "Upgrade with: scoop update easy-sso"
	case "winget":
		return "Upgrade through the WinGet package that installed this executable."
	case "go":
		return "Upgrade with: go install github.com/ThamalDilanka/easy-sso/cmd/easysso@v" + version
	case "direct":
		return "Upgrade with: easysso update (Windows: close all instances and replace manually from " + ReleaseURL(version) + ")"
	default:
		return "Installation provenance is unknown; reinstall using the verified release archive and its installation metadata: " + ReleaseURL(version)
	}
}

// Replace is deliberately available only after acquiring an exclusive lease.
// Rename is the commit point; every reported failure leaves the original intact.
func (l *Lease) Replace(binary []byte) error {
	if !l.exclusive {
		return errors.New("replacement requires an exclusive executable lease")
	}
	if len(binary) == 0 {
		return errors.New("empty update")
	}
	if err := l.current(); err != nil {
		return err
	}
	info, err := l.file.Stat()
	if err != nil {
		return err
	}
	stage, err := os.CreateTemp(filepath.Dir(l.path), ".easy-sso-update-*")
	if err != nil {
		return errors.New("cannot stage update beside executable; check installation directory permissions")
	}
	name := stage.Name()
	defer os.Remove(name)
	defer stage.Close()
	if _, err = stage.Write(binary); err != nil {
		return err
	}
	if err = stage.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err = stage.Sync(); err != nil {
		return err
	}
	if err = stage.Close(); err != nil {
		return err
	}
	if err = l.current(); err != nil {
		return err
	}
	if err = l.rename(name, l.path); err != nil {
		return errors.New("could not replace executable; installed executable unchanged")
	}
	return nil
}
