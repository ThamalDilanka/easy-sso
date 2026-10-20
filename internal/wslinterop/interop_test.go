package wslinterop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// No test may run cmd.exe or wslpath.
func init() {
	RunInterop = func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		panic("unexpected interop call: " + name + " " + strings.Join(args, " "))
	}
	LookPath = func(string) (string, error) { return "", errors.New("not found") }
}

func utf16le(s string) []byte {
	var out []byte
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func TestParseWindowsHome(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{utf16le("D:\\winhome\r\n"), `D:\winhome`},
		{[]byte("D:\\winhome\r\n"), `D:\winhome`},
		{utf16le("D:\\exämple\r\n"), `D:\exämple`},
	} {
		if got, ok := ParseWindowsHome(tc.in); !ok || got != tc.want {
			t.Errorf("ParseWindowsHome(%q) = %q, %v", tc.in, got, ok)
		}
	}
	for _, bad := range [][]byte{utf16le("%USERPROFILE%\r\n"), []byte(`\\server\share`), nil, []byte(`D:\a\..\b`), []byte("D:\\a\x01b"), []byte("D:\\" + strings.Repeat("a", 4100))} {
		if got, ok := ParseWindowsHome(bad); ok {
			t.Errorf("ParseWindowsHome(%q) accepted %q", bad, got)
		}
	}
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
	oldRun, oldLook := RunInterop, LookPath
	t.Cleanup(func() { RunInterop, LookPath = oldRun, oldLook; ResetHome() })
	ResetHome()
	LookPath = func(string) (string, error) { return "", errors.New("appendWindowsPath=false") }
	RunInterop = func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
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

func TestWindowsHomeLookup(t *testing.T) {
	home, calls := fakeWindows(t, utf16le("D:\\winhome\r\n"), nil)
	linux, win, err := Home(context.Background(), true)
	if err != nil || linux != home || win != `D:\winhome` {
		t.Fatalf("Home = %q %q %v", linux, win, err)
	}
	cmdCall := (*calls)[1]
	if !strings.HasSuffix(cmdCall, "cmd.exe /d /u /c echo %USERPROFILE%") || !strings.HasPrefix(cmdCall, filepath.Dir(strings.Fields(strings.SplitN(cmdCall, "|", 2)[1])[0])+"|") {
		t.Fatalf("cmd.exe call %q", cmdCall)
	}
	if _, _, err = Home(context.Background(), true); err != nil || len(*calls) != 3 {
		t.Fatalf("success not cached: %d calls, %v", len(*calls), err)
	}

	_, calls = fakeWindows(t, nil, errors.New("exec format error"))
	if _, _, err = Home(context.Background(), false); !errors.Is(err, ErrNoWindowsHome) {
		t.Fatalf("interop failure: %v", err)
	}
	n := len(*calls)
	if _, _, err = Home(context.Background(), false); !errors.Is(err, ErrNoWindowsHome) || len(*calls) != n {
		t.Fatal("passive lookup retried a failure")
	}
	if _, _, _ = Home(context.Background(), true); len(*calls) == n {
		t.Fatal("explicit lookup did not retry")
	}

	home, _ = fakeWindows(t, []byte(`D:\winhome`), nil)
	_ = os.Remove(home)
	_ = os.WriteFile(home, nil, 0o644)
	if _, _, err = Home(context.Background(), true); !errors.Is(err, ErrNoWindowsHome) {
		t.Fatalf("home that is a file: %v", err)
	}
}

func TestDetected(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if !Detected("linux", env(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"})) {
		t.Error("linux with WSL_DISTRO_NAME not detected")
	}
	if Detected("linux", env(nil)) || Detected("windows", env(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"})) || Detected("darwin", env(map[string]string{"WSL_DISTRO_NAME": "x"})) {
		t.Error("false positive")
	}
}

func TestWindowsEnvRefusesUnsafeNames(t *testing.T) {
	fakeWindows(t, nil, nil)
	RunInterop = func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		t.Errorf("interop reached with unsafe name: %s %v", name, args)
		return nil, nil
	}
	for _, bad := range []string{"", "A&calc", "A B", "A|B", "A%B", "1ABC", "A\"B", "A;B", "A\nB", "A^B", "A<B", "ProgramFiles(x86)&"} {
		if _, err := WindowsEnv(context.Background(), bad); err == nil {
			t.Errorf("WindowsEnv(%q) accepted", bad)
		}
	}
}

func TestWindowsEnvValueAndUnset(t *testing.T) {
	_, calls := fakeWindows(t, utf16le("C:\\Program Files (x86)\r\n"), nil)
	got, err := WindowsEnv(context.Background(), "ProgramFiles(x86)")
	if err != nil || got != `C:\Program Files (x86)` {
		t.Fatalf("WindowsEnv = %q %v", got, err)
	}
	if last := (*calls)[len(*calls)-1]; !strings.HasSuffix(last, "cmd.exe /d /u /c echo %ProgramFiles(x86)%") {
		t.Fatalf("cmd.exe call %q", last)
	}
}

func TestWindowsEnvUnset(t *testing.T) {
	fakeWindows(t, utf16le("%NOPE%\r\n"), nil)
	got, err := WindowsEnv(context.Background(), "NOPE")
	if err != nil || got != "" {
		t.Fatalf("unset = %q %v", got, err)
	}
	fakeWindows(t, nil, errors.New("exec format error"))
	if _, err = WindowsEnv(context.Background(), "NOPE"); err == nil {
		t.Fatal("interop failure not reported")
	}
}

func TestPathConversion(t *testing.T) {
	var got []string
	old := RunInterop
	t.Cleanup(func() { RunInterop = old })
	RunInterop = func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		got = append(got, name+" "+strings.Join(args, " "))
		return []byte("/out\n"), nil
	}
	if p, err := ToLinux(context.Background(), `C:\x`); err != nil || p != "/out" {
		t.Fatalf("ToLinux = %q %v", p, err)
	}
	if p, err := ToWindows(context.Background(), "/x"); err != nil || p != "/out" {
		t.Fatalf("ToWindows = %q %v", p, err)
	}
	if got[0] != `wslpath -u C:\x` || got[1] != "wslpath -w /x" {
		t.Fatalf("calls %q", got)
	}
	RunInterop = func(context.Context, string, string, ...string) ([]byte, error) { return nil, errors.New("x") }
	if _, err := ToLinux(context.Background(), "a"); err == nil {
		t.Fatal("error swallowed")
	}
}
