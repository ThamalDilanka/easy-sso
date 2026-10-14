package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPrivateAtomicStorageAndLock(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(d, "state.json")
	if e := AtomicWrite(path, []byte("first")); e != nil {
		t.Fatal(e)
	}
	if e := AtomicWrite(path, []byte("second")); e != nil {
		t.Fatal(e)
	}
	b, e := Read(path, 20)
	if e != nil || string(b) != "second" {
		t.Fatal(string(b), e)
	}
	if _, e = Read(path, 2); e == nil {
		t.Fatal("limit ignored")
	}
	u, e := Lock(path + ".lock")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Lock(path + ".lock"); e == nil {
		t.Fatal("double writer")
	}
	u()
	if _, e = Lock(path + ".lock"); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(path)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatal("unsafe file mode")
	}
}
func TestSymlinkAndUnsafePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink semantics")
	}
	d := t.TempDir()
	target := filepath.Join(d, "target")
	os.WriteFile(target, []byte("original"), 0600)
	link := filepath.Join(d, "link")
	os.Symlink(target, link)
	if e := AtomicWrite(link, []byte("new")); e == nil {
		t.Fatal("symlink accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "original" {
		t.Fatal("target overwritten")
	}
	unsafe := filepath.Join(d, "unsafe")
	os.Mkdir(unsafe, 0755)
	if e := EnsureDir(unsafe); e == nil {
		t.Fatal("unsafe directory accepted")
	}
}
