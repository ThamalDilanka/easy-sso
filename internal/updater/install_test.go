package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvenanceAndGuidance(t *testing.T) {
	for _, tc := range []struct{ path, marker, dist, want, command string }{
		{"direct/easysso", "direct", "release", "direct", "easysso update"},
		{"Cellar/easy-sso/1.0/libexec/easysso", "direct", "release", "homebrew", "brew upgrade"},
		{"scoop/apps/easy-sso/1.0/easysso.exe", "direct", "release", "scoop", "scoop update"},
		{"Microsoft/WinGet/Packages/easy-sso/easysso.exe", "direct", "release", "winget", "WinGet"},
		{"go/easysso", "direct", "go", "go", "go install"},
		{"missing/easysso", "", "release", "unknown", "unknown"},
		{"dev/easysso", "direct", "development", "unknown", "unknown"},
		{"manager/easysso", "homebrew", "release", "homebrew", "brew upgrade"},
	} {
		t.Run(tc.want+tc.path, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), filepath.FromSlash(tc.path))
			os.MkdirAll(filepath.Dir(path), 0755)
			os.WriteFile(path, []byte("binary"), 0755)
			if tc.marker != "" {
				os.WriteFile(filepath.Join(filepath.Dir(path), "easy-sso.install.json"), []byte(`{"channel":"`+tc.marker+`","repository":"ThamalDilanka/easy-sso"}`), 0644)
			}
			got, e := Inspect(path, tc.dist)
			if e != nil || got.Channel != tc.want || !strings.Contains(got.Guidance("1.0.0"), tc.command) {
				t.Fatalf("%+v %v", got, e)
			}
		})
	}
}
