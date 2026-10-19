package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func isBundle(b []byte) bool { return bytes.HasPrefix(b, []byte("BUNDLE")) }

func leftovers(t *testing.T, dir string) {
	t.Helper()
	if m, _ := filepath.Glob(filepath.Join(dir, ".easy-sso-*")); len(m) != 0 {
		t.Fatalf("staging files left behind: %v", m)
	}
}

func TestPublicReplace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("drvfs permissions are a WSL concern")
	}
	// Like a drvfs mount: every file and folder reports mode 0777.
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rds-ca-aws.pem")
	changed, err := PublicReplace(path, []byte("BUNDLE one"), isBundle)
	if err != nil || !changed {
		t.Fatalf("create: %v %v", changed, err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	_ = os.Chtimes(path, old, old)
	if changed, err = PublicReplace(path, []byte("BUNDLE one"), isBundle); err != nil || changed {
		t.Fatalf("identical: %v %v", changed, err)
	}
	if st, _ := os.Stat(path); !st.ModTime().Equal(old) {
		t.Fatal("identical bytes rewrote the file")
	}
	if changed, err = PublicReplace(path, []byte("BUNDLE two"), isBundle); err != nil || !changed {
		t.Fatalf("replace: %v %v", changed, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "BUNDLE two" {
		t.Fatalf("content %q", b)
	}
	leftovers(t, dir)

	foreign := filepath.Join(dir, "notes.pem")
	_ = os.WriteFile(foreign, []byte("my notes"), 0o644)
	if _, err = PublicReplace(foreign, []byte("BUNDLE one"), isBundle); !errors.Is(err, ErrNotReplaceable) {
		t.Fatalf("foreign file: %v", err)
	}
	if b, _ := os.ReadFile(foreign); string(b) != "my notes" {
		t.Fatal("foreign file overwritten")
	}
	sub := filepath.Join(dir, "folder.pem")
	_ = os.Mkdir(sub, 0o755)
	if _, err = PublicReplace(sub, []byte("BUNDLE one"), isBundle); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory target: %v", err)
	}
	link := filepath.Join(dir, "link.pem")
	_ = os.Symlink(path, link)
	if _, err = PublicReplace(link, []byte("BUNDLE three"), isBundle); !errors.Is(err, ErrLink) {
		t.Fatalf("symlink target: %v", err)
	}
	linkedDir := filepath.Join(dir, "linked")
	_ = os.Symlink(sub, linkedDir)
	if _, err = PublicReplace(filepath.Join(linkedDir, "ca.pem"), []byte("BUNDLE one"), isBundle); !errors.Is(err, ErrLink) {
		t.Fatalf("symlinked parent: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "BUNDLE two" {
		t.Fatal("link followed")
	}
	leftovers(t, dir)
}
