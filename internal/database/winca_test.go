package database

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ThamalDilanka/easy-sso/internal/store"
	"github.com/ThamalDilanka/easy-sso/internal/wslinterop"
)

// No database test may run cmd.exe or wslpath.
func init() {
	wslinterop.RunInterop = func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		panic("unexpected interop call: " + name + " " + strings.Join(args, " "))
	}
	wslinterop.LookPath = func(string) (string, error) { return "", errors.New("not found") }
}

func utf16le(s string) []byte {
	var out []byte
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// fakeWindows wires the interop seams to a temp "drive" and records calls.
func fakeWindows(t *testing.T, homeOutput []byte, homeErr error) (home string, calls *[]string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home = filepath.Join(root, "winhome")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := filepath.Join(root, "System32", "cmd.exe")
	_ = os.MkdirAll(filepath.Dir(cmd), 0o755)
	_ = os.WriteFile(cmd, nil, 0o755)
	calls = &[]string{}
	oldRun, oldLook := wslinterop.RunInterop, wslinterop.LookPath
	t.Cleanup(func() { wslinterop.RunInterop, wslinterop.LookPath = oldRun, oldLook; wslinterop.ResetHome() })
	wslinterop.ResetHome()
	wslinterop.LookPath = func(string) (string, error) { return "", errors.New("appendWindowsPath=false") }
	wslinterop.RunInterop = func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, dir+"|"+name+" "+strings.Join(args, " "))
		switch {
		case name == "wslpath" && args[1] == `C:\Windows\System32\cmd.exe`:
			return []byte(cmd + "\n"), nil
		case name == cmd:
			return homeOutput, homeErr
		case name == "wslpath" && args[1] == `D:\winhome`:
			return []byte(home + "\n"), nil
		}
		t.Errorf("unexpected interop %s %v", name, args)
		return nil, errors.New("unexpected")
	}
	return home, calls
}

func bundle(t *testing.T) []byte {
	t.Helper()
	srv := httptest.NewTLSServer(nil)
	srv.Close()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

func TestExportCAToWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("WSL only")
	}
	home, _ := fakeWindows(t, utf16le("D:\\winhome\r\n"), nil)
	state := stateDir(t)
	src := filepath.Join(state, "rds-ca-aws.pem")
	ca := bundle(t)
	_ = os.WriteFile(src, ca, 0o600)
	dest := filepath.Join(home, ".easy-sso", "rds-ca-aws.pem")

	got, err := ExportCAToWindows(context.Background(), src, true)
	if err != nil || got.Exists {
		t.Fatalf("refresh-only before export: %+v %v", got, err)
	}
	if _, err = os.Stat(filepath.Dir(dest)); !os.IsNotExist(err) {
		t.Fatal("refresh-only created the folder")
	}
	got, err = ExportCAToWindows(context.Background(), src, false)
	if err != nil || !got.Exists || !got.Changed || got.Windows != `D:\winhome\.easy-sso\rds-ca-aws.pem` || got.Linux != dest {
		t.Fatalf("export: %+v %v", got, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(ca) {
		t.Fatal("exported bytes differ")
	}
	if got, err = ExportCAToWindows(context.Background(), src, false); err != nil || got.Changed {
		t.Fatalf("idempotent: %+v %v", got, err)
	}
	renewed := append(append([]byte{}, ca...), bundle(t)...)
	_ = os.WriteFile(src, renewed, 0o600)
	if got, err = ExportCAToWindows(context.Background(), src, true); err != nil || !got.Changed {
		t.Fatalf("refresh of a renewed bundle: %+v %v", got, err)
	}
	_ = os.WriteFile(dest, []byte("someone else's file"), 0o644)
	if _, err = ExportCAToWindows(context.Background(), src, false); !errors.Is(err, ErrNotCABundle) {
		t.Fatalf("foreign file: %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "someone else's file" {
		t.Fatal("foreign file overwritten")
	}
	bad := filepath.Join(state, "bad.pem")
	_ = os.WriteFile(bad, []byte("not a bundle"), 0o600)
	if _, err = ExportCAToWindows(context.Background(), bad, false); err == nil || !strings.Contains(err.Error(), "CA file unavailable or invalid") {
		t.Fatalf("invalid source: %v", err)
	}
	custom := filepath.Join(state, "my ca.txt")
	_ = os.WriteFile(custom, ca, 0o600)
	if got, err = ExportCAToWindows(context.Background(), custom, false); err != nil || !regexp.MustCompile(`^D:\\winhome\\\.easy-sso\\my_ca\.txt-[0-9a-f]{8}\.pem$`).MatchString(got.Windows) {
		t.Fatalf("custom name: %+v %v", got, err)
	}
	// Same-named files from two folders never share an export.
	other := filepath.Join(state, "other")
	_ = os.Mkdir(other, 0o700)
	_ = os.WriteFile(filepath.Join(other, "my ca.txt"), ca, 0o600)
	if again, e := ExportCAToWindows(context.Background(), filepath.Join(other, "my ca.txt"), false); e != nil || again.Windows == got.Windows {
		t.Fatalf("same basename shares %q: %v", again.Windows, e)
	}
	// A source reached through a link (e.g. ~/Downloads -> /mnt/c/...) is
	// public content and still exports; only destination links are refused.
	_ = os.Remove(dest) // the foreign file from above
	_ = os.Symlink(state, filepath.Join(filepath.Dir(state), "linked-state"))
	if _, e := ExportCAToWindows(context.Background(), filepath.Join(filepath.Dir(state), "linked-state", "rds-ca-aws.pem"), false); e != nil {
		t.Fatalf("linked source: %v", e)
	}
	if !CachedCA(filepath.Join(filepath.Dir(state), "linked-state", "rds-ca-aws.pem")) {
		t.Fatal("CachedCA refuses a linked source")
	}
	// A link in place of the folder is refused, never followed.
	_ = os.RemoveAll(filepath.Join(home, ".easy-sso"))
	_ = os.Symlink(state, filepath.Join(home, ".easy-sso"))
	if _, err = ExportCAToWindows(context.Background(), src, false); !errors.Is(err, store.ErrLink) {
		t.Fatalf("linked folder: %v", err)
	}
}
