package sessions

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/launch"
)

// fakeLauncher records every call; it never starts a process.
type fakeLauncher struct {
	mu           sync.Mutex
	found        map[launch.App][]launch.Install
	findErr      map[launch.App]error
	finds        []launch.App
	running      bool
	runErr       error
	runningCalls int
	starts       []launch.Request
	onStart      func()
	startCtxErr  error
	startErr     error
}

func (f *fakeLauncher) Find(_ context.Context, app launch.App) ([]launch.Install, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finds = append(f.finds, app)
	return f.found[app], f.findErr[app]
}

func (f *fakeLauncher) Running(context.Context, launch.Install) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runningCalls++
	return f.running, f.runErr
}

func (f *fakeLauncher) Start(ctx context.Context, r launch.Request) error {
	// The caller's ctx may end during the DataGrip handover wait.
	if f.onStart != nil {
		f.onStart()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCtxErr = ctx.Err()
	f.starts = append(f.starts, r)
	return f.startErr
}

var (
	localDataGrip  = launch.Install{App: launch.DataGrip, Place: launch.Local, Path: "/opt/datagrip/bin/datagrip.sh"}
	localCompass   = launch.Install{App: launch.Compass, Place: launch.Local, Path: "/usr/bin/mongodb-compass"}
	windowsCompass = launch.Install{App: launch.Compass, Place: launch.Windows, Path: "/mnt/c/Apps/MongoDBCompass/MongoDBCompass.exe"}
)

// launchManager returns a connected docdb-iam run, a recording launcher and a
// Credentials view that still lists the run's role.
func launchManager(t *testing.T) (*Manager, domain.Run, domain.Connection, domain.ManagedRole, domain.Snapshot, *fakeLauncher) {
	t.Helper()
	m, c, role, s, _, _, _ := docdbManager(t, "", false)
	m.options.RecoveryCredentials = func(string) (domain.ManagedRole, domain.Snapshot, error) { return role, s, nil }
	run, err := m.Open(context.Background(), c, role, s)
	if err != nil || run.State != "Connected" {
		t.Fatal(run.State, err)
	}
	f := &fakeLauncher{}
	m.options.Launcher = f
	return m, run, c, role, s, f
}

// mutate changes a live entry for one test and restores it before Shutdown,
// which needs the owned child and state to stop the fake tunnel.
func mutate(t *testing.T, m *Manager, id string, change func(*entry)) {
	t.Helper()
	en, err := m.find(id)
	if err != nil {
		t.Fatal(err)
	}
	en.mu.Lock()
	run, snapshot, child, recovered := copyRun(en.run), en.snapshot, en.child, en.recovered
	change(en)
	en.mu.Unlock()
	t.Cleanup(func() {
		en.mu.Lock()
		en.run, en.snapshot, en.child, en.recovered = run, snapshot, child, recovered
		en.mu.Unlock()
	})
}

func (f *fakeLauncher) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runningCalls, len(f.starts)
}

func TestLaunchClientAppUsesFrozenSnapshot(t *testing.T) {
	m, run, c, role, s, f := launchManager(t)
	renewed := s
	renewed.AccessKeyID, renewed.SecretAccessKey, renewed.SessionToken = "ASIARENEWED", "RENEWED-SECRET", "RENEWED-TOKEN"
	renewed.Expiration = s.Expiration.Add(time.Hour)
	renewedRole := role
	renewedRole.Expiration = renewed.Expiration
	m.options.RecoveryCredentials = func(string) (domain.ManagedRole, domain.Snapshot, error) { return renewedRole, renewed, nil }

	exp, err := m.LaunchClientApp(context.Background(), run.ID, localDataGrip)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(s.Expiration) {
		t.Fatalf("expiry = %v, want the frozen %v", exp, s.Expiration)
	}
	if len(f.starts) != 1 {
		t.Fatal("client not started once", len(f.starts))
	}
	r := f.starts[0]
	if r.Install != localDataGrip || len(r.Args) != 0 {
		t.Fatalf("DataGrip request = %+v", r)
	}
	want := map[string]string{"AWS_ACCESS_KEY_ID": s.AccessKeyID, "AWS_SECRET_ACCESS_KEY": s.SecretAccessKey, "AWS_SESSION_TOKEN": s.SessionToken, "AWS_REGION": c.Region, "AWS_CONFIG_FILE": "/dev/null", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null"}
	for key, value := range want {
		if got, ok := envValue(r.Secret, key); !ok || got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
	for _, kv := range r.Secret {
		if strings.Contains(kv, "RENEWED") {
			t.Fatal("renewed credentials reached the client")
		}
	}
}

func TestLaunchClientAppRefusesRuns(t *testing.T) {
	cases := map[string]func(t *testing.T) (*Manager, string, *fakeLauncher){
		"recovered": func(t *testing.T) (*Manager, string, *fakeLauncher) {
			m, run, _, _, _, f := launchManager(t)
			mutate(t, m, run.ID, func(en *entry) { en.recovered = true })
			return m, run.ID, f
		},
		"unowned": func(t *testing.T) (*Manager, string, *fakeLauncher) {
			m, run, _, _, _, f := launchManager(t)
			mutate(t, m, run.ID, func(en *entry) { en.child = nil })
			return m, run.ID, f
		},
		"no own snapshot": func(t *testing.T) (*Manager, string, *fakeLauncher) {
			m, run, _, _, _, f := launchManager(t)
			mutate(t, m, run.ID, func(en *entry) { en.snapshot = domain.Snapshot{} })
			return m, run.ID, f
		},
		"not listening": func(t *testing.T) (*Manager, string, *fakeLauncher) {
			m, run, _, _, _, f := launchManager(t)
			mutate(t, m, run.ID, func(en *entry) { en.run.State = "Stopped" })
			return m, run.ID, f
		},
		"unknown run": func(t *testing.T) (*Manager, string, *fakeLauncher) {
			m, _, _, _, _, f := launchManager(t)
			return m, runID(), f
		},
		"tunnel mode": func(t *testing.T) (*Manager, string, *fakeLauncher) {
			m, c, role, s, _, _, _ := docdbManager(t, "", false)
			m.options.RecoveryCredentials = func(string) (domain.ManagedRole, domain.Snapshot, error) { return role, s, nil }
			c.AuthMode, c.Database = "", ""
			run, err := m.Open(context.Background(), c, role, s)
			if err != nil || run.State != "Listening" {
				t.Fatal(run.State, err)
			}
			f := &fakeLauncher{}
			m.options.Launcher = f
			return m, run.ID, f
		},
		"postgres-iam": func(t *testing.T) (*Manager, string, *fakeLauncher) {
			m, c, role, s, _, _, _ := dbManager(t, "")
			m.options.RecoveryCredentials = func(string) (domain.ManagedRole, domain.Snapshot, error) { return role, s, nil }
			run, err := m.Open(context.Background(), c, role, s)
			if err != nil || run.State != "Connected" {
				t.Fatal(run.State, err)
			}
			f := &fakeLauncher{}
			m.options.Launcher = f
			return m, run.ID, f
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			m, id, f := setup(t)
			if _, err := m.LaunchClientApp(context.Background(), id, localDataGrip); err == nil {
				t.Fatal("launch accepted")
			}
			if running, starts := f.counts(); running != 0 || starts != 0 {
				t.Fatal("refused launch reached the launcher", running, starts)
			}
		})
	}
}

func TestLaunchClientAppRefusesRemovedRole(t *testing.T) {
	for _, missing := range []bool{false, true} {
		m, run, _, _, _, f := launchManager(t)
		if missing {
			m.options.RecoveryCredentials = nil
		} else {
			m.options.RecoveryCredentials = func(string) (domain.ManagedRole, domain.Snapshot, error) {
				return domain.ManagedRole{}, domain.Snapshot{}, errors.New("role not found")
			}
		}
		_, err := m.LaunchClientApp(context.Background(), run.ID, localDataGrip)
		if err == nil || err.Error() != "Your role was removed or unpublished; log in again before launching a client" {
			t.Fatalf("missing=%v: err = %v", missing, err)
		}
		if running, starts := f.counts(); running != 0 || starts != 0 {
			t.Fatal("refused launch reached the launcher", running, starts)
		}
	}
}

func TestLaunchClientAppRefusesShortLife(t *testing.T) {
	m, run, _, _, s, f := launchManager(t)
	soon := time.Now().Add(5 * time.Minute).UTC()
	mutate(t, m, run.ID, func(en *entry) { en.snapshot.Expiration = soon })
	_, err := m.LaunchClientApp(context.Background(), run.ID, localDataGrip)
	want := "Credentials expire at " + soon.Local().Format("15:04") + "; renew the role and reconnect before launching a client"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	for _, secret := range []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("refusal carries a secret")
		}
	}
	if running, starts := f.counts(); running != 0 || starts != 0 {
		t.Fatal("refused launch reached the launcher", running, starts)
	}
}

func TestLaunchClientAppRefusesOpenDataGrip(t *testing.T) {
	for name, tc := range map[string]struct {
		running bool
		err     error
		want    string
	}{
		"open":    {running: true, want: "Close DataGrip, then press g again."},
		"unknown": {err: launch.ErrRunningUnknown, want: launch.ErrRunningUnknown.Error()},
		"other":   {err: errors.New("tasklist output"), want: launch.ErrRunningUnknown.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			m, run, _, _, _, f := launchManager(t)
			f.running, f.runErr = tc.running, tc.err
			_, err := m.LaunchClientApp(context.Background(), run.ID, localDataGrip)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if running, starts := f.counts(); running != 1 || starts != 0 {
				t.Fatal("DataGrip check skipped or client started", running, starts)
			}
		})
	}
}

func TestLaunchClientAppRefusedOnNativeWindows(t *testing.T) {
	m, run, _, _, _, f := launchManager(t)
	old := hostOS
	hostOS = "windows"
	t.Cleanup(func() { hostOS = old })
	_, err := m.LaunchClientApp(context.Background(), run.ID, localDataGrip)
	if !errors.Is(err, launch.ErrNativeWindows) {
		t.Fatalf("err = %v", err)
	}
	if running, starts := f.counts(); running != 0 || starts != 0 {
		t.Fatal("native Windows reached the launcher", running, starts)
	}
}

func TestLaunchClientAppCompassArgs(t *testing.T) {
	const winCA = `C:\Profiles\u\.easy-sso\ca-0a1b2c3d.pem`
	query := func(ca string) string {
		return "?tls=true&tlsCAFile=" + strings.ReplaceAll(url.QueryEscape(ca), "+", "%20") + "&directConnection=true&retryWrites=false&tlsAllowInvalidHostnames=true&authSource=%24external&authMechanism=MONGODB-AWS"
	}
	for _, tc := range []struct {
		name    string
		in      launch.Install
		db      string
		null    string
		windows bool
	}{
		{name: "local", in: localCompass, db: "app_db", null: "/dev/null"},
		{name: "local no database", in: localCompass, null: "/dev/null"},
		{name: "windows", in: windowsCompass, db: "app_db", null: "NUL", windows: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, run, c, _, s, f := launchManager(t)
			mutate(t, m, run.ID, func(en *entry) { en.run.Connection.Database = tc.db })
			var exported []string
			m.exportCA = func(_ context.Context, src string, refreshOnly bool) (domain.CAExport, error) {
				exported = append(exported, src+"|"+strconv.FormatBool(refreshOnly))
				return domain.CAExport{Windows: winCA, Linux: "/testhome/.easy-sso/ca-0a1b2c3d.pem", Exists: true}, nil
			}
			if _, err := m.LaunchClientApp(context.Background(), run.ID, tc.in); err != nil {
				t.Fatal(err)
			}
			if len(f.starts) != 1 {
				t.Fatal("Compass not started once")
			}
			r := f.starts[0]
			ca := c.CAFile
			if tc.windows {
				ca = winCA
				if len(exported) != 1 || exported[0] != c.CAFile+"|false" {
					t.Fatal("CA not exported to Windows from the cached bundle", exported)
				}
			} else if len(exported) != 0 {
				t.Fatal("local Compass exported the CA", exported)
			}
			path := "/"
			if tc.db != "" {
				path += tc.db
			}
			want := "mongodb://127.0.0.1:" + strconv.Itoa(c.LocalPort) + path + query(ca)
			if len(r.Args) != 1 || r.Args[0] != want {
				t.Fatalf("args = %q\nwant  %q", r.Args, want)
			}
			for _, secret := range []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken} {
				if strings.Contains(r.Args[0], secret) {
					t.Fatal("Compass URI carries a secret")
				}
			}
			if strings.Contains(r.Args[0], "@") {
				t.Fatal("Compass URI carries a user")
			}
			for _, key := range []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"} {
				if got, _ := envValue(r.Secret, key); got != tc.null {
					t.Fatalf("%s = %q, want %q", key, got, tc.null)
				}
			}
		})
	}
	t.Run("windows export fails", func(t *testing.T) {
		m, run, _, _, _, f := launchManager(t)
		m.exportCA = func(context.Context, string, bool) (domain.CAExport, error) {
			return domain.CAExport{}, errors.New("interop off")
		}
		if _, err := m.LaunchClientApp(context.Background(), run.ID, windowsCompass); err == nil || strings.Contains(err.Error(), "interop off") {
			t.Fatalf("err = %v, want a fixed text", err)
		}
		if _, starts := f.counts(); starts != 0 {
			t.Fatal("Compass started without a CA path")
		}
	})
}

func TestLaunchClientAppStartOutlivesCallerContext(t *testing.T) {
	m, run, _, _, _, f := launchManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onStart = cancel
	if _, err := m.LaunchClientApp(ctx, run.ID, localDataGrip); err != nil {
		t.Fatal(err)
	}
	if f.startCtxErr != nil {
		t.Fatal("Start's context ended with the caller's:", f.startCtxErr)
	}
	f.onStart, f.startErr = nil, launch.ErrHandedOver
	if _, err := m.LaunchClientApp(context.Background(), run.ID, localDataGrip); !errors.Is(err, launch.ErrHandedOver) {
		t.Fatalf("start failure = %v", err)
	}
}

func TestClientApps(t *testing.T) {
	m, _, _, _, _, f := launchManager(t)
	windowsDataGrip := launch.Install{App: launch.DataGrip, Place: launch.Windows, Path: "/mnt/c/Apps/DataGrip/bin/datagrip64.exe"}
	f.found = map[launch.App][]launch.Install{launch.DataGrip: {localDataGrip, windowsDataGrip}, launch.Compass: {localCompass}}
	got, err := m.ClientApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != localDataGrip || got[1] != windowsDataGrip || got[2] != localCompass {
		t.Fatalf("installs = %+v", got)
	}
	if len(f.finds) != 2 || f.finds[0] != launch.DataGrip || f.finds[1] != launch.Compass {
		t.Fatal("find order", f.finds)
	}
	f.findErr = map[launch.App]error{launch.DataGrip: errors.New("lookup failed")}
	got, err = m.ClientApps(context.Background())
	if err != nil || len(got) != 1 || got[0] != localCompass {
		t.Fatalf("a failed DataGrip lookup hid Compass: %+v %v", got, err)
	}
}
