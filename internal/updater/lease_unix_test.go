//go:build !windows

package updater

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplacementAndActivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysso")
	os.WriteFile(path, []byte("old"), 0751)
	first, e := Acquire(path, false)
	if e != nil {
		t.Fatal(e)
	}
	second, e := Acquire(path, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Acquire(path, true); e == nil {
		t.Fatal("updated active app")
	}
	first.Close()
	second.Close()
	l, e := Acquire(path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if _, e = Acquire(path, false); e == nil {
		t.Fatal("app started during update")
	}
	l.rename = func(string, string) error { return errors.New("simulated failed rename") }
	if e = l.Replace([]byte("new")); e == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "old" {
		t.Fatal("failed replacement destroyed original")
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 {
		t.Fatal("staging file leaked")
	}
	l.rename = os.Rename
	if e = l.Replace([]byte("new")); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(path)
	st, _ := os.Stat(path)
	if string(b) != "new" || st.Mode().Perm() != 0751 {
		t.Fatal("wrong content or mode")
	}
	if e = l.Replace([]byte("again")); e == nil {
		t.Fatal("stale inode replaced")
	}
}
func TestSymlinkProvenance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Cellar", "easy-sso", "1.0", "easysso")
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("binary"), 0755)
	link := filepath.Join(dir, "easysso")
	os.Symlink(path, link)
	i, e := Inspect(link, "release")
	if e != nil || i.Channel != "homebrew" || i.Path != path {
		t.Fatalf("%+v %v", i, e)
	}
}
