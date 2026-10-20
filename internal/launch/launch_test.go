package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ThamalDilanka/easy-sso/internal/wslinterop"
)

// No test may run cmd.exe, wslpath, tasklist.exe or pgrep.
func init() {
	wslinterop.RunInterop = func(_ context.Context, _, name string, args ...string) ([]byte, error) {
		panic("unexpected interop call: " + name + " " + strings.Join(args, " "))
	}
	wslinterop.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	pgrep = func(context.Context, string) (bool, error) { panic("unexpected pgrep call") }
}

const (
	testKey     = "ASIATESTKEY"
	testSecret  = "TOP-SECRET-VALUE"
	testToken   = "SESSION-TOKEN-VALUE"
	wantWSLENV  = "AWS_ACCESS_KEY_ID/w:AWS_SECRET_ACCESS_KEY/w:AWS_SESSION_TOKEN/w:AWS_REGION/w:AWS_CONFIG_FILE/w:AWS_SHARED_CREDENTIALS_FILE/w:AWS_EC2_METADATA_DISABLED/w:AWS_PROFILE/w:AWS_DEFAULT_PROFILE/w"
	compassArgs = "mongodb://example.invalid:27017/?authMechanism=MONGODB-AWS&tls=true"
)

func secretEnv(null string) []string {
	return []string{"AWS_ACCESS_KEY_ID=" + testKey, "AWS_SECRET_ACCESS_KEY=" + testSecret, "AWS_SESSION_TOKEN=" + testToken, "AWS_REGION=us-east-1", "AWS_CONFIG_FILE=" + null, "AWS_SHARED_CREDENTIALS_FILE=" + null, "AWS_EC2_METADATA_DISABLED=true"}
}

func ambientEnv() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=/testhome", "DISPLAY=:0", "WSLENV=USERVAR/u", "ELECTRON_RUN_AS_NODE=1",
		"AWS_PROFILE=ambient", "AWS_DEFAULT_PROFILE=ambient", "AWS_ROLE_ARN=ambient-role", "AWS_WEB_IDENTITY_TOKEN_FILE=/tmp/ambient-token",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI=http://example.invalid/creds", "AWS_ACCESS_KEY_ID=AMBIENTKEY", "AWS_SECRET_ACCESS_KEY=AMBIENTSECRET", "AWS_CONFIG_FILE=/testhome/.aws/config"}
}

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, e := range env {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			t.Fatalf("env entry without '=': %q", e)
		}
		if _, dup := m[k]; dup {
			t.Fatalf("duplicate env key %s", k)
		}
		m[k] = v
	}
	return m
}

func TestBuildEnvPerPlatform(t *testing.T) {
	winExe := "/mnt/c/Program Files/JetBrains/DataGrip 2025.1.3/bin/datagrip64.exe"
	for _, tc := range []struct {
		name    string
		goos    string
		wsl     bool
		in      Install
		args    []string
		null    string
		windows bool
	}{
		{"linux", "linux", false, Install{DataGrip, Local, "/opt/datagrip/bin/datagrip.sh"}, nil, "/dev/null", false},
		{"darwin", "darwin", false, Install{Compass, Local, "/Applications/MongoDB Compass.app/Contents/MacOS/MongoDB Compass"}, []string{compassArgs}, "/dev/null", false},
		{"wsl-local", "linux", true, Install{Compass, Local, "/usr/bin/mongodb-compass"}, []string{compassArgs}, "/dev/null", false},
		{"wsl-windows", "linux", true, Install{DataGrip, Windows, winExe}, nil, "NUL", true},
		{"wsl-windows-compass", "linux", true, Install{Compass, Windows, "/mnt/c/Profiles/u/AppData/Local/MongoDBCompass/MongoDBCompass.exe"}, []string{compassArgs}, "NUL", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := build(tc.goos, tc.wsl, Request{Install: tc.in, Args: tc.args, Secret: secretEnv(tc.null)}, ambientEnv())
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if spec.Path != tc.in.Path || !reflect.DeepEqual(spec.Args, tc.args) {
				t.Fatalf("path/args = %q %q", spec.Path, spec.Args)
			}
			for _, a := range spec.Args {
				for _, s := range []string{testKey, testSecret, testToken} {
					if strings.Contains(a, s) {
						t.Fatalf("secret in args: %q", a)
					}
				}
			}
			env := envMap(t, spec.Env)
			want := envMap(t, secretEnv(tc.null))
			if tc.windows {
				want["AWS_PROFILE"], want["AWS_DEFAULT_PROFILE"] = "", ""
			}
			for k, v := range env {
				if strings.HasPrefix(k, "AWS_") && want[k] != v {
					t.Errorf("%s=%q leaked or wrong", k, v)
				}
			}
			for k, v := range want {
				if got, ok := env[k]; !ok || got != v {
					t.Errorf("%s = %q, %v; want %q", k, got, ok, v)
				}
			}
			if _, ok := env["ELECTRON_RUN_AS_NODE"]; ok {
				t.Error("ELECTRON_RUN_AS_NODE kept")
			}
			if env["PATH"] != "/usr/bin:/bin" || env["DISPLAY"] != ":0" {
				t.Error("ordinary variables dropped")
			}
			wslenv, ok := env["WSLENV"]
			if tc.windows && wslenv != wantWSLENV {
				t.Errorf("WSLENV = %q", wslenv)
			}
			if !tc.windows && ok {
				t.Errorf("WSLENV present: %q", wslenv)
			}
			hasNUL := strings.Contains(strings.Join(spec.Env, "\n"), "=NUL\n") || strings.HasSuffix(strings.Join(spec.Env, "\n"), "=NUL")
			if hasNUL != tc.windows {
				t.Errorf("NUL present = %v", hasNUL)
			}
			if tc.windows && spec.Dir != filepath.Dir(tc.in.Path) {
				t.Errorf("Dir = %q", spec.Dir)
			}
			if tc.windows && !strings.HasPrefix(spec.Dir, "/mnt/c/") {
				t.Errorf("Dir not a /mnt/<d> folder: %q", spec.Dir)
			}
		})
	}

	// Ruling R-T7 and the guards around the request.
	good := Request{Install: Install{DataGrip, Windows, winExe}, Secret: secretEnv("NUL")}
	for name, tc := range map[string]struct {
		goos string
		wsl  bool
		r    Request
	}{
		"windows target with /dev/null": {"linux", true, Request{Install: good.Install, Secret: secretEnv("/dev/null")}},
		"local target with NUL":         {"linux", false, Request{Install: Install{DataGrip, Local, "/opt/datagrip/bin/datagrip.sh"}, Secret: secretEnv("NUL")}},
		"darwin target with NUL":        {"darwin", false, Request{Install: Install{Compass, Local, "/Applications/MongoDB Compass.app/Contents/MacOS/MongoDB Compass"}, Secret: secretEnv("NUL")}},
		"missing null device":           {"linux", true, Request{Install: good.Install, Secret: secretEnv("NUL")[:4]}},
		"mixed null devices":            {"linux", true, Request{Install: good.Install, Secret: append(secretEnv("NUL")[:5], "AWS_SHARED_CREDENTIALS_FILE=/dev/null", "AWS_EC2_METADATA_DISABLED=true")}},
		"secret in args":                {"linux", true, Request{Install: good.Install, Args: []string{"--token=" + testToken}, Secret: good.Secret}},
		"windows place without wsl":     {"linux", false, good},
		"windows place on darwin":       {"darwin", false, good},
		"windows place outside /mnt":    {"linux", true, Request{Install: Install{DataGrip, Windows, "/tmp/datagrip64.exe"}, Secret: good.Secret}},
		"windows cmd target":            {"linux", true, Request{Install: Install{DataGrip, Windows, "/mnt/c/tools/datagrip.cmd"}, Secret: good.Secret}},
		"windows bat target":            {"linux", true, Request{Install: Install{DataGrip, Windows, "/mnt/c/tools/datagrip.bat"}, Secret: good.Secret}},
		"local exe target":              {"linux", true, Request{Install: Install{DataGrip, Local, "/mnt/c/tools/datagrip64.exe"}, Secret: secretEnv("/dev/null")}},
		"relative path":                 {"linux", false, Request{Install: Install{DataGrip, Local, "bin/datagrip"}, Secret: secretEnv("/dev/null")}},
		"newline in path":               {"linux", false, Request{Install: Install{DataGrip, Local, "/opt/data\ngrip"}, Secret: secretEnv("/dev/null")}},
		"dotdot path":                   {"linux", true, Request{Install: Install{DataGrip, Windows, "/mnt/c/../../tmp/x.exe"}, Secret: good.Secret}},
		"unknown app":                   {"linux", false, Request{Install: Install{"psql", Local, "/usr/bin/psql"}, Secret: secretEnv("/dev/null")}},
		"non-AWS secret":                {"linux", false, Request{Install: Install{DataGrip, Local, "/opt/datagrip/bin/datagrip.sh"}, Secret: append(secretEnv("/dev/null"), "LD_PRELOAD=/tmp/x.so")}},
		"native windows":                {"windows", false, Request{Install: Install{DataGrip, Local, `C:\x\datagrip64.exe`}, Secret: secretEnv("NUL")}},
	} {
		spec, err := build(tc.goos, tc.wsl, tc.r, ambientEnv())
		if err == nil {
			t.Errorf("%s: accepted: %+v", name, spec)
			continue
		}
		for _, s := range []string{testKey, testSecret, testToken} {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: secret in error %q", name, err)
			}
		}
	}
}

func TestStartRefusedOnNativeWindows(t *testing.T) {
	old := goos
	t.Cleanup(func() { goos = old })
	goos = "windows"
	err := System{}.Start(context.Background(), Request{Install: Install{DataGrip, Local, `C:\x\datagrip64.exe`}, Secret: secretEnv("NUL")})
	if err == nil || err.Error() != "DocumentDB IAM login needs Linux, macOS or WSL" {
		t.Fatalf("Start on native Windows = %v", err)
	}
	if !errors.Is(err, ErrNativeWindows) {
		t.Fatal("not ErrNativeWindows")
	}
	if got, err := (System{}).Find(context.Background(), DataGrip); err != nil || len(got) != 0 {
		t.Fatalf("Find on native Windows = %v %v", got, err)
	}
}

// --- Find ---

type fakeEnv map[string]string

func (f fakeEnv) get(k string) string { return f[k] }

// withSeams points the package at a fake file tree and environment.
func withSeams(t *testing.T, os_ string, env fakeEnv, path map[string]string) string {
	t.Helper()
	root := t.TempDir()
	oldGoos, oldGetenv, oldRoot, oldLook := goos, getenv, fsRoot, lookPath
	oldRun, oldWLook := wslinterop.RunInterop, wslinterop.LookPath
	t.Cleanup(func() {
		goos, getenv, fsRoot, lookPath = oldGoos, oldGetenv, oldRoot, oldLook
		wslinterop.RunInterop, wslinterop.LookPath = oldRun, oldWLook
	})
	goos, getenv, fsRoot = os_, env.get, root
	lookPath = func(name string) (string, error) {
		if p, ok := path[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	return root
}

func touch(t *testing.T, root, p string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func utf16le(s string) []byte {
	var out []byte
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// fakeInterop answers cmd.exe echo and wslpath -u like a Windows host.
func fakeInterop(t *testing.T, vars map[string]string, paths map[string]string) *[]string {
	t.Helper()
	calls := &[]string{}
	cmd := "/mnt/c/Windows/System32/cmd.exe"
	wslinterop.LookPath = func(name string) (string, error) {
		if name == "cmd.exe" {
			return cmd, nil
		}
		return "", errors.New("not found")
	}
	wslinterop.RunInterop = func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		switch {
		case name == cmd && len(args) == 5 && args[3] == "echo":
			key := strings.Trim(args[4], "%")
			if v, ok := vars[key]; ok {
				return utf16le(v + "\r\n"), nil
			}
			return utf16le(args[4] + "\r\n"), nil
		case name == "wslpath" && args[0] == "-u":
			if p, ok := paths[args[1]]; ok {
				return []byte(p + "\n"), nil
			}
			return nil, errors.New("wslpath failed")
		}
		t.Errorf("unexpected interop %s %v", name, args)
		return nil, errors.New("unexpected")
	}
	return calls
}

func TestFindPlaces(t *testing.T) {
	ctx := context.Background()
	find := func(app App) []Install {
		t.Helper()
		got, err := System{}.Find(ctx, app)
		if err != nil {
			t.Fatalf("Find(%s): %v", app, err)
		}
		return got
	}

	t.Run("linux", func(t *testing.T) {
		root := withSeams(t, "linux", fakeEnv{"HOME": "/testhome"}, nil)
		touch(t, root, "testhome/.local/share/JetBrains/Toolbox/scripts/datagrip", 0o755)
		touch(t, root, "opt/datagrip/bin/datagrip.sh", 0o755)
		touch(t, root, "usr/bin/mongodb-compass", 0o644) // not executable
		if got := find(DataGrip); !reflect.DeepEqual(got, []Install{{DataGrip, Local, "/testhome/.local/share/JetBrains/Toolbox/scripts/datagrip"}}) {
			t.Fatalf("DataGrip = %+v", got)
		}
		if got := find(Compass); len(got) != 0 {
			t.Fatalf("non-executable Compass found: %+v", got)
		}
	})

	t.Run("linux PATH first", func(t *testing.T) {
		root := withSeams(t, "linux", fakeEnv{"HOME": "/testhome"}, map[string]string{"datagrip.sh": "/usr/local/bin/datagrip.sh", "mongodb-compass": "/usr/local/bin/mongodb-compass"})
		touch(t, root, "usr/local/bin/datagrip.sh", 0o755)
		touch(t, root, "usr/local/bin/mongodb-compass", 0o755)
		touch(t, root, "snap/bin/datagrip", 0o755)
		touch(t, root, "usr/bin/mongodb-compass", 0o755)
		if got := find(DataGrip); !reflect.DeepEqual(got, []Install{{DataGrip, Local, "/usr/local/bin/datagrip.sh"}}) {
			t.Fatalf("DataGrip = %+v", got)
		}
		if got := find(Compass); !reflect.DeepEqual(got, []Install{{Compass, Local, "/usr/local/bin/mongodb-compass"}}) {
			t.Fatalf("Compass = %+v", got)
		}
	})

	t.Run("darwin", func(t *testing.T) {
		root := withSeams(t, "darwin", fakeEnv{"HOME": "/testhome"}, nil)
		touch(t, root, "testhome/Applications/DataGrip.app/Contents/MacOS/datagrip", 0o755)
		touch(t, root, "Applications/MongoDB Compass.app/Contents/MacOS/MongoDB Compass", 0o755)
		if got := find(DataGrip); !reflect.DeepEqual(got, []Install{{DataGrip, Local, "/testhome/Applications/DataGrip.app/Contents/MacOS/datagrip"}}) {
			t.Fatalf("DataGrip = %+v", got)
		}
		if got := find(Compass); !reflect.DeepEqual(got, []Install{{Compass, Local, "/Applications/MongoDB Compass.app/Contents/MacOS/MongoDB Compass"}}) {
			t.Fatalf("Compass = %+v", got)
		}
	})

	wslVars := map[string]string{"LOCALAPPDATA": `C:\Profiles\u\AppData\Local`, "ProgramFiles": `C:\Program Files`}
	wslPaths := map[string]string{`C:\Profiles\u\AppData\Local`: "/mnt/c/Profiles/u/AppData/Local", `C:\Program Files`: "/mnt/c/Program Files"}
	windowsTree := func(t *testing.T, root string) {
		touch(t, root, "mnt/c/Program Files/JetBrains/DataGrip 2025.9/bin/datagrip64.exe", 0o755)
		touch(t, root, "mnt/c/Program Files/JetBrains/DataGrip 2025.10/bin/datagrip64.exe", 0o755)
		touch(t, root, "mnt/c/Program Files/JetBrains/DataGrip 2026.1/bin/datagrip64.cmd", 0o755)
		touch(t, root, "mnt/c/Program Files/JetBrains/DataGrip 2026.2/bin/datagrip64.bat", 0o755)
		touch(t, root, "mnt/c/Profiles/u/AppData/Local/MongoDBCompass/MongoDBCompass.exe", 0o755)
	}

	t.Run("wsl both places", func(t *testing.T) {
		root := withSeams(t, "linux", fakeEnv{"HOME": "/testhome", "WSL_DISTRO_NAME": "Distro", "WAYLAND_DISPLAY": "wayland-0"},
			map[string]string{"datagrip": "/mnt/c/tools/datagrip", "datagrip.sh": "/mnt/c/tools/datagrip.cmd"})
		fakeInterop(t, wslVars, wslPaths)
		windowsTree(t, root)
		touch(t, root, "mnt/c/tools/datagrip", 0o755)
		touch(t, root, "mnt/c/tools/datagrip.cmd", 0o755)
		touch(t, root, "opt/datagrip/bin/datagrip.sh", 0o755)
		touch(t, root, "usr/bin/mongodb-compass", 0o755)
		want := []Install{{DataGrip, Local, "/opt/datagrip/bin/datagrip.sh"}, {DataGrip, Windows, "/mnt/c/Program Files/JetBrains/DataGrip 2025.10/bin/datagrip64.exe"}}
		if got := find(DataGrip); !reflect.DeepEqual(got, want) {
			t.Fatalf("DataGrip = %+v", got)
		}
		want = []Install{{Compass, Local, "/usr/bin/mongodb-compass"}, {Compass, Windows, "/mnt/c/Profiles/u/AppData/Local/MongoDBCompass/MongoDBCompass.exe"}}
		if got := find(Compass); !reflect.DeepEqual(got, want) {
			t.Fatalf("Compass = %+v", got)
		}
	})

	t.Run("wsl user install wins", func(t *testing.T) {
		root := withSeams(t, "linux", fakeEnv{"WSL_DISTRO_NAME": "Distro"}, nil)
		fakeInterop(t, wslVars, wslPaths)
		windowsTree(t, root)
		touch(t, root, "mnt/c/Profiles/u/AppData/Local/Programs/DataGrip/bin/datagrip64.exe", 0o755)
		touch(t, root, "mnt/c/Program Files/MongoDB Compass/MongoDBCompass.exe", 0o755)
		touch(t, root, "opt/datagrip/bin/datagrip.sh", 0o755) // no DISPLAY: not offered
		if got := find(DataGrip); !reflect.DeepEqual(got, []Install{{DataGrip, Windows, "/mnt/c/Profiles/u/AppData/Local/Programs/DataGrip/bin/datagrip64.exe"}}) {
			t.Fatalf("DataGrip = %+v", got)
		}
		if got := find(Compass); !reflect.DeepEqual(got, []Install{{Compass, Windows, "/mnt/c/Profiles/u/AppData/Local/MongoDBCompass/MongoDBCompass.exe"}}) {
			t.Fatalf("Compass = %+v", got)
		}
	})

	t.Run("wsl only cmd on Windows", func(t *testing.T) {
		root := withSeams(t, "linux", fakeEnv{"WSL_DISTRO_NAME": "Distro"}, nil)
		fakeInterop(t, wslVars, wslPaths)
		touch(t, root, "mnt/c/Profiles/u/AppData/Local/Programs/DataGrip/bin/datagrip64.cmd", 0o755)
		touch(t, root, "mnt/c/Program Files/JetBrains/DataGrip 2026.1/bin/datagrip64.cmd", 0o755)
		if got := find(DataGrip); len(got) != 0 {
			t.Fatalf(".cmd offered: %+v", got)
		}
	})

	for name, bad := range map[string]string{"relative": "Profiles/u", "outside /mnt": "/tmp/u", "newline": "/mnt/c/Profiles/u\n/x", "dotdot": "/mnt/c/../../tmp", "unc": "//wsl.localhost/x"} {
		t.Run("wsl bad converted path "+name, func(t *testing.T) {
			root := withSeams(t, "linux", fakeEnv{"WSL_DISTRO_NAME": "Distro"}, nil)
			fakeInterop(t, wslVars, map[string]string{`C:\Profiles\u\AppData\Local`: bad, `C:\Program Files`: bad})
			touch(t, root, strings.TrimPrefix(filepath.Join(bad, "MongoDBCompass/MongoDBCompass.exe"), "/"), 0o755)
			if got := find(Compass); len(got) != 0 {
				t.Fatalf("bad converted path used: %+v", got)
			}
		})
	}

	t.Run("wsl interop off", func(t *testing.T) {
		root := withSeams(t, "linux", fakeEnv{"WSL_DISTRO_NAME": "Distro", "DISPLAY": ":0"}, nil)
		wslinterop.RunInterop = func(context.Context, string, string, ...string) ([]byte, error) {
			return nil, errors.New("exec format error")
		}
		touch(t, root, "usr/bin/mongodb-compass", 0o755)
		if got := find(Compass); !reflect.DeepEqual(got, []Install{{Compass, Local, "/usr/bin/mongodb-compass"}}) {
			t.Fatalf("Compass = %+v", got)
		}
	})

	t.Run("unknown app", func(t *testing.T) {
		withSeams(t, "linux", fakeEnv{}, nil)
		if _, err := (System{}).Find(ctx, "psql"); err == nil {
			t.Fatal("unknown app accepted")
		}
	})
}

// --- Running ---

func TestRunningDataGrip(t *testing.T) {
	ctx := context.Background()
	winDG := Install{DataGrip, Windows, "/mnt/c/Program Files/JetBrains/DataGrip 2025.1.3/bin/datagrip64.exe"}
	tasklist := "/mnt/c/Windows/System32/tasklist.exe"
	wantArgs := []string{"/FI", "IMAGENAME eq datagrip64.exe", "/FO", "CSV", "/NH"}

	for _, tc := range []struct {
		name    string
		out     string
		err     error
		running bool
		fail    bool
	}{
		{"found", "\"datagrip64.exe\",\"4242\",\"Console\",\"1\",\"812,000 K\"\r\n", nil, true, false},
		{"found upper case", "\"DataGrip64.EXE\",\"4242\",\"Console\",\"1\",\"812,000 K\"\r\n", nil, true, false},
		{"not found", "INFO: No tasks are running which match the specified criteria.\r\n", nil, false, false},
		{"error", "", errors.New("exit status 1"), false, true},
		{"empty output", "", nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withSeams(t, "linux", fakeEnv{"WSL_DISTRO_NAME": "Distro"}, nil)
			var got []string
			var gotDir string
			wslinterop.LookPath = func(name string) (string, error) {
				if name == "tasklist.exe" {
					return tasklist, nil
				}
				return "", errors.New("not found")
			}
			wslinterop.RunInterop = func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
				if name != tasklist {
					t.Fatalf("unexpected interop %s %v", name, args)
				}
				got, gotDir = args, dir
				return []byte(tc.out), tc.err
			}
			running, err := System{}.Running(ctx, winDG)
			if !reflect.DeepEqual(got, wantArgs) || gotDir != "/mnt/c/Windows/System32" {
				t.Fatalf("tasklist call %q in %q", got, gotDir)
			}
			if tc.fail {
				if err == nil || err.Error() != "easy-sso could not check whether DataGrip is open. Close it, then press g again." {
					t.Fatalf("check failure = %v %v", running, err)
				}
				return
			}
			if err != nil || running != tc.running {
				t.Fatalf("Running = %v %v", running, err)
			}
		})
	}

	t.Run("tasklist via wslpath", func(t *testing.T) {
		root := withSeams(t, "linux", fakeEnv{"WSL_DISTRO_NAME": "Distro"}, nil)
		touch(t, root, "mnt/c/Windows/System32/tasklist.exe", 0o755)
		var called bool
		wslinterop.RunInterop = func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
			switch {
			case name == "wslpath" && args[1] == `C:\Windows\System32\tasklist.exe`:
				return []byte(tasklist + "\n"), nil
			case name == tasklist:
				called = true
				return []byte("INFO: none\r\n"), nil
			}
			t.Fatalf("unexpected interop %s %v", name, args)
			return nil, nil
		}
		if running, err := (System{}).Running(ctx, winDG); err != nil || running || !called {
			t.Fatalf("Running = %v %v (called %v)", running, err, called)
		}
	})

	t.Run("compass never checked", func(t *testing.T) {
		withSeams(t, "linux", fakeEnv{"WSL_DISTRO_NAME": "Distro"}, nil)
		wslinterop.RunInterop = func(_ context.Context, _, name string, args ...string) ([]byte, error) {
			t.Fatalf("Compass checked: %s %v", name, args)
			return nil, nil
		}
		fsRoot = "/nonexistent-root"
		for _, in := range []Install{{Compass, Windows, "/mnt/c/Profiles/u/AppData/Local/MongoDBCompass/MongoDBCompass.exe"}, {Compass, Local, "/usr/bin/mongodb-compass"}} {
			if running, err := (System{}).Running(ctx, in); err != nil || running {
				t.Fatalf("Running(%+v) = %v %v", in, running, err)
			}
		}
	})

	proc := func(t *testing.T, root, pid, comm string, argv ...string) {
		t.Helper()
		dir := filepath.Join(root, "proc", pid)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
	}
	local := Install{DataGrip, Local, "/opt/datagrip/bin/datagrip.sh"}
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, root string)
		running bool
		fail    bool
	}{
		{"no datagrip", func(t *testing.T, root string) {
			proc(t, root, "100", "bash", "bash")
			proc(t, root, "101", "java", "/opt/idea/jbr/bin/java", "-Didea.platform.prefix=Idea", "com.intellij.idea.Main")
			_ = os.MkdirAll(filepath.Join(root, "proc", "self"), 0o755)
		}, false, false},
		{"native launcher", func(t *testing.T, root string) {
			proc(t, root, "100", "bash", "bash")
			proc(t, root, "200", "datagrip", "/opt/datagrip/bin/datagrip")
		}, true, false},
		{"script launcher", func(t *testing.T, root string) {
			proc(t, root, "300", "sh", "/bin/sh", "/testhome/.local/share/JetBrains/Toolbox/scripts/datagrip")
		}, true, false},
		{"java main with DataGrip home", func(t *testing.T, root string) {
			proc(t, root, "400", "java", "/opt/datagrip/jbr/bin/java", "-Didea.platform.prefix=DataGrip", "com.intellij.idea.Main")
		}, true, false},
		{"no proc", func(t *testing.T, root string) {}, false, true},
	} {
		t.Run("linux "+tc.name, func(t *testing.T) {
			root := withSeams(t, "linux", fakeEnv{}, nil)
			tc.setup(t, root)
			running, err := System{}.Running(ctx, local)
			if tc.fail {
				if err == nil || !errors.Is(err, ErrRunningUnknown) {
					t.Fatalf("check failure = %v %v", running, err)
				}
				return
			}
			if err != nil || running != tc.running {
				t.Fatalf("Running = %v %v", running, err)
			}
		})
	}

	for _, tc := range []struct {
		name    string
		found   bool
		err     error
		running bool
	}{{"found", true, nil, true}, {"not found", false, nil, false}, {"error", false, errors.New("pgrep failed"), false}} {
		t.Run("darwin "+tc.name, func(t *testing.T) {
			withSeams(t, "darwin", fakeEnv{}, nil)
			old := pgrep
			t.Cleanup(func() { pgrep = old })
			var pattern string
			pgrep = func(_ context.Context, p string) (bool, error) { pattern = p; return tc.found, tc.err }
			running, err := System{}.Running(ctx, Install{DataGrip, Local, "/Applications/DataGrip.app/Contents/MacOS/datagrip"})
			if pattern != "DataGrip.app/Contents/MacOS/datagrip" {
				t.Fatalf("pattern %q", pattern)
			}
			if tc.err != nil {
				if !errors.Is(err, ErrRunningUnknown) {
					t.Fatalf("check failure = %v", err)
				}
				return
			}
			if err != nil || running != tc.running {
				t.Fatalf("Running = %v %v", running, err)
			}
		})
	}
}

func TestNewerName(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"DataGrip 2025.10", "DataGrip 2025.9", true},
		{"DataGrip 2025.9", "DataGrip 2025.10", false},
		{"DataGrip 2025.1.3", "DataGrip 2025.1", true},
		{"DataGrip 2026.1", "DataGrip 2025.3.2", true},
		{"DataGrip 2025.1", "DataGrip 2025.1", false},
	} {
		if got := newerName(tc.a, tc.b); got != tc.want {
			t.Errorf("newerName(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}
