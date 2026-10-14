// Package releases owns the archive contract shared with tools/package.py.
package releases

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

//go:embed contract.json
var files embed.FS
var Contract struct {
	Repository        string            `json:"repository"`
	Targets           []string          `json:"targets"`
	Documents         []string          `json:"documents"`
	ArchivePattern    string            `json:"archive_pattern"`
	ArchiveExtensions map[string]string `json:"archive_extensions"`
	VersionPattern    string            `json:"version_pattern"`
}

func init() {
	b, err := files.ReadFile("contract.json")
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(b, &Contract); err != nil {
		panic(err)
	}
	versionPattern = regexp.MustCompile(Contract.VersionPattern)
}

// Version accepts complete SemVer, with an optional v prefix. Numeric strings
// are compared by length to avoid integer overflow for valid large components.
type Version struct {
	core [3]string
	pre  []string
	Text string
}

var versionPattern *regexp.Regexp

func Parse(s string) (Version, error) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("invalid semantic version")
	}
	v := Version{core: [3]string{m[1], m[2], m[3]}, Text: strings.TrimPrefix(s, "v")}
	for i := 4; i <= 5; i++ {
		if m[i] == "" {
			continue
		}
		for _, p := range strings.Split(m[i], ".") {
			if p == "" || (i == 4 && numeric(p) && len(p) > 1 && p[0] == '0') {
				return Version{}, fmt.Errorf("invalid semantic version identifier")
			}
		}
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	return v, nil
}
func numeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func numberCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}
func (v Version) Prerelease() bool { return len(v.pre) > 0 }
func (v Version) Development() bool {
	for _, p := range v.pre {
		if p == "dev" || p == "dirty" {
			return true
		}
	}
	// Go pseudo-versions contain a timestamp and revision, not a release tag.
	return regexp.MustCompile(`(?:^|\.)[0-9]{14}-[0-9a-f]{12}$`).MatchString(strings.Join(v.pre, "."))
}
func (v Version) Compare(w Version) int {
	for i := range v.core {
		if c := numberCompare(v.core[i], w.core[i]); c != 0 {
			return c
		}
	}
	if len(v.pre) == 0 && len(w.pre) > 0 {
		return 1
	}
	if len(w.pre) == 0 && len(v.pre) > 0 {
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(w.pre); i++ {
		a, b := v.pre[i], w.pre[i]
		var c int
		if numeric(a) && numeric(b) {
			c = numberCompare(a, b)
		} else if numeric(a) {
			c = -1
		} else if numeric(b) {
			c = 1
		} else {
			c = strings.Compare(a, b)
		}
		if c != 0 {
			return c
		}
	}
	if len(v.pre) < len(w.pre) {
		return -1
	}
	if len(v.pre) > len(w.pre) {
		return 1
	}
	return 0
}
func Asset(version, goos, arch string) (string, error) {
	v, err := Parse(version)
	if err != nil {
		return "", err
	}
	target := goos + "-" + arch
	for _, t := range Contract.Targets {
		if t == target {
			name := strings.NewReplacer("{version}", v.Text, "{target}", target).Replace(Contract.ArchivePattern)
			return name + Contract.ArchiveExtensions[goos], nil
		}
	}
	return "", fmt.Errorf("no release package for %s", target)
}
func Binary(goos string) string {
	if goos == "windows" {
		return "easysso.exe"
	}
	return "easysso"
}

// Stamp is set by the release build. Go omits -ldflags from build information
// under -trimpath, so verification uses this explicit record instead.
var Stamp = "development"

const StampPrefix = "easy-sso-release-v1|"

func StampHeader(version, goos, arch string) string {
	return StampPrefix + version + "|" + goos + "|" + arch + "|"
}
