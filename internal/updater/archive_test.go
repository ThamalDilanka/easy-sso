package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/releases"
)

func tarFixture(t *testing.T, extra *tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	names := append([]string{"easysso", "easy-sso.install.json"}, releases.Contract.Documents...)
	for _, name := range names {
		data := []byte("fixture")
		h := &tar.Header{Name: name, Size: int64(len(data)), Mode: 0755}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write(data)
	}
	if extra != nil {
		if err := tw.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	// Oversize fixtures intentionally end in an incomplete entry.
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}
func TestArchiveSafety(t *testing.T) {
	good := tarFixture(t, nil)
	if b, e := extract(good, "linux"); e != nil || string(b) != "fixture" {
		t.Fatal(e)
	}
	for _, h := range []*tar.Header{{Name: "../easysso", Typeflag: tar.TypeReg}, {Name: "/easysso", Typeflag: tar.TypeReg}, {Name: "easysso", Typeflag: tar.TypeReg}, {Name: "docs/USAGE.md", Typeflag: tar.TypeSymlink, Linkname: "../../secret"}, {Name: "unexpected", Typeflag: tar.TypeLink, Linkname: "easy-sso"}, {Name: "extra", Typeflag: tar.TypeReg, Size: 200 << 20}} {
		if _, e := extract(tarFixture(t, h), "linux"); e == nil {
			t.Fatalf("accepted %+v", h)
		}
	}
	var oversized bytes.Buffer
	gz := gzip.NewWriter(&oversized)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "easysso", Typeflag: tar.TypeReg, Size: 97 << 20, Mode: 0755})
	_ = tw.Close()
	_ = gz.Close()
	if _, err := extract(oversized.Bytes(), "linux"); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized binary: %v", err)
	}
	var linked bytes.Buffer
	zw := zip.NewWriter(&linked)
	header := &zip.FileHeader{Name: "easysso.exe"}
	header.SetMode(os.ModeSymlink | 0755)
	entry, _ := zw.CreateHeader(header)
	_, _ = entry.Write([]byte("outside"))
	_ = zw.Close()
	if _, err := extract(linked.Bytes(), "windows"); err == nil {
		t.Fatal("ZIP symlink accepted")
	}
	if _, e := extract(good[:len(good)-5], "linux"); e == nil {
		t.Fatal("accepted truncated gzip")
	}
	if _, e := verifyArchive(good, strings.Repeat("0", 64), Candidate{goos: "linux"}); e == nil || !strings.Contains(e.Error(), "checksum") {
		t.Fatal(e)
	}
	var z bytes.Buffer
	w := zip.NewWriter(&z)
	f, _ := w.Create("../easysso.exe")
	f.Write([]byte("bad"))
	w.Close()
	if _, e := extract(z.Bytes(), "windows"); e == nil {
		t.Fatal("ZIP traversal accepted")
	}
}
func TestManifestExactness(t *testing.T) {
	c := Candidate{Version: "1.2.3", archive: asset{Name: "easy-sso-1.2.3-linux-amd64.tar.gz"}}
	var b strings.Builder
	for _, target := range releases.Contract.Targets {
		p := strings.Split(target, "-")
		name, _ := releases.Asset(c.Version, p[0], p[1])
		fmt.Fprintf(&b, "%s  %s\n", strings.Repeat("a", 64), name)
	}
	valid := b.String()
	if _, e := checksum([]byte(valid), c); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{valid + strings.Split(valid, "\n")[0] + "\n", strings.Replace(valid, "linux-amd64", "linux-armv7", 1), strings.Replace(valid, "aaaa", "zzzz", 1), strings.Split(valid, "\n")[0]} {
		if _, e := checksum([]byte(bad), c); e == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}

// This gate consumes actual tools/package.py output for all five targets, then
// drives the real HTTP/download/verification/replacement path in a temp install.
func TestPackagedRelease(t *testing.T) {
	dir := os.Getenv("EASY_SSO_RELEASE_DIR")
	if dir == "" {
		t.Skip("set EASY_SSO_RELEASE_DIR after make package")
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	if len(lines) != len(releases.Contract.Targets) {
		t.Fatal("wrong artifact count")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != len(lines)+1 {
		t.Fatal("unexpected release assets", err)
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		name := fields[1]
		var goos, arch, ver string
		for _, target := range releases.Contract.Targets {
			p := strings.Split(target, "-")
			ext := ".tar.gz"
			if p[0] == "windows" {
				ext = ".zip"
			}
			suffix := "-" + target + ext
			if strings.HasSuffix(name, suffix) {
				goos, arch = p[0], p[1]
				ver = strings.TrimSuffix(strings.TrimPrefix(name, "easy-sso-"), suffix)
			}
		}
		if ver == "" {
			t.Fatalf("unrecognized asset %s", name)
		}
		t.Run(goos+"-"+arch, func(t *testing.T) {
			data, e := os.ReadFile(filepath.Join(dir, name))
			if e != nil {
				t.Fatal(e)
			}
			c := Candidate{Version: ver, goos: goos, arch: arch, archive: asset{Name: name, ID: 1}, sums: asset{Name: "SHA256SUMS", ID: 2}}
			client := fakeClient(t, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/repos/ThamalDilanka/easy-sso/releases/assets/1":
					return response(200, string(data)), nil
				case "/repos/ThamalDilanka/easy-sso/releases/assets/2":
					return response(200, string(sums)), nil
				}
				t.Fatal(r.URL)
				return nil, nil
			})
			binary, e := client.Download(context.Background(), c)
			if e != nil {
				t.Fatal(e)
			}
			wrong := c
			wrong.arch = "wrong"
			if e = validateBinary(binary, wrong); e == nil {
				t.Fatal("wrong architecture accepted")
			}
			wrong = c
			wrong.Version = "999.0.0"
			if e = validateBinary(binary, wrong); e == nil {
				t.Fatal("wrong version accepted")
			}
			if goos == runtime.GOOS && arch == runtime.GOARCH && goos != "windows" {
				path := filepath.Join(t.TempDir(), "easysso")
				if e = os.WriteFile(path, []byte("old executable"), 0755); e != nil {
					t.Fatal(e)
				}
				lease, e := Acquire(path, true)
				if e != nil {
					t.Fatal(e)
				}
				defer lease.Close()
				if e = lease.Replace(binary); e != nil {
					t.Fatal(e)
				}
				installed, _ := os.ReadFile(path)
				actual := sha256.Sum256(installed)
				want := sha256.Sum256(binary)
				if hex.EncodeToString(actual[:]) != hex.EncodeToString(want[:]) {
					t.Fatal("wrong installed executable")
				}
				output, err := exec.Command(path, "version").CombinedOutput()
				if err != nil || !strings.Contains(string(output), "easy-sso "+ver+" (") {
					t.Fatalf("installed version: %s %v", output, err)
				}
				output, err = exec.Command(path, "--help").CombinedOutput()
				if err != nil || !strings.Contains(string(output), "update [--check]") {
					t.Fatalf("installed startup: %s %v", output, err)
				}

			}
		})
	}
}
