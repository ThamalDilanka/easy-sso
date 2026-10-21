package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/launch"
	sessionsvc "github.com/ThamalDilanka/easy-sso/internal/sessions"
)

// The real sessions service offers both optional interfaces the UI looks for.
var (
	_ clientLister   = (*sessionsvc.Manager)(nil)
	_ clientLauncher = (*sessionsvc.Manager)(nil)
)

var (
	dataGripLocal   = launch.Install{App: launch.DataGrip, Place: launch.Local, Path: "/opt/datagrip/bin/datagrip.sh"}
	dataGripWindows = launch.Install{App: launch.DataGrip, Place: launch.Windows, Path: "/mnt/c/Program Files/JetBrains/DataGrip/bin/datagrip64.exe"}
	compassLocal    = launch.Install{App: launch.Compass, Place: launch.Local, Path: "/usr/bin/mongodb-compass"}
)

type launchCall struct {
	runID string
	in    launch.Install
}

// launchSessions adds the optional client launcher to the fake sessions.
type launchSessions struct {
	*fakeSessions
	installs []launch.Install
	lookups  int
	launched []launchCall
	expiry   time.Time
	err      error
}

func (s *launchSessions) ClientApps(context.Context) ([]launch.Install, error) {
	s.lookups++
	return append([]launch.Install(nil), s.installs...), nil
}

func (s *launchSessions) LaunchClientApp(_ context.Context, runID string, in launch.Install) (time.Time, error) {
	s.launched = append(s.launched, launchCall{runID, in})
	if s.err != nil {
		return time.Time{}, s.err
	}
	return s.expiry, nil
}

// iamRun is a connected docdb-iam run with a verified assumed-role identity.
func iamRun(f fixture) domain.Run {
	c := docdbIAMBookmark(f)
	c.LocalPort = 37018
	p, _ := f.m.profile()
	id := domain.Identity{Account: p.AccountID, ARN: "arn:aws:sts::" + p.AccountID + ":assumed-role/AWSReservedSSO_ReadOnly_0123456789abcdef/dev", UserID: "id:dev"}
	return domain.Run{ID: "run-iam", Connection: c, Profile: p, Alias: "managed-one", Identity: id, State: "Connected", SessionID: "exact-session", StartedAt: time.Now()}
}

// setupLaunch selects a docdb-iam bookmark in Databases whose run is live.
func setupLaunch(t *testing.T, installs ...launch.Install) (fixture, *launchSessions, domain.Run) {
	t.Helper()
	f := setupUI(t)
	ls := &launchSessions{fakeSessions: f.sessions, installs: installs, expiry: time.Date(2026, 10, 21, 15, 4, 0, 0, time.Local)}
	f.m.services.Sessions = ls
	run := iamRun(f)
	bookmark := docdbIAMBookmark(f)
	f.sessions.runs = []domain.Run{run}
	f.conns.items = []domain.Connection{bookmark}
	finish(f.m, f.m.load())
	f.m.screen, f.m.focus, f.m.selectedRow = databases, 1, 0
	return f, ls, run
}

func hasControl(m *Model, label, key string) bool {
	for _, c := range m.controls() {
		if c.key == key && c.label == label {
			return true
		}
	}
	return false
}

func TestLaunchKeyAndMouseShareHandler(t *testing.T) {
	for _, via := range []string{"key", "mouse"} {
		for _, from := range []string{"bookmark", "bookmark review", "run", "run review"} {
			t.Run(via+"/"+from, func(t *testing.T) {
				f, ls, run := setupLaunch(t, dataGripLocal, compassLocal)
				switch from {
				case "bookmark review":
					key(f.m, "v")
				case "run":
					f.m.screen, f.m.selectedRow = tunnels, 0
				case "run review":
					f.m.screen, f.m.selectedRow = tunnels, 0
					key(f.m, "v")
				}
				if !hasControl(f.m, "Open client", "g") {
					t.Fatalf("no Open client control: %+v", f.m.controls())
				}
				press1 := func(k string) {
					if via == "key" {
						key(f.m, k)
					} else {
						clickHit(f.m, mouseHit(t, f.m, "key", k, 0))
					}
				}
				press1("g")
				if ls.lookups != 1 || f.m.review == nil || f.m.review.kind != "client-app" || f.m.review.run.ID != run.ID {
					t.Fatalf("lookups %d review %+v status %q", ls.lookups, f.m.review, f.m.status)
				}
				content := strings.Join(f.m.reviewContent(), "\n")
				for _, want := range []string{"DataGrip", "MongoDB Compass"} {
					if !strings.Contains(content, want) {
						t.Fatalf("picker lacks %q:\n%s", want, content)
					}
				}
				if !hasControl(f.m, "DataGrip", "d") || !hasControl(f.m, "MongoDB Compass", "m") {
					t.Fatalf("picker controls %+v", f.m.controls())
				}
				press1("m")
				if len(ls.launched) != 1 || ls.launched[0] != (launchCall{run.ID, compassLocal}) {
					t.Fatalf("launched %+v status %q", ls.launched, f.m.status)
				}
				if f.m.review != nil || !strings.HasPrefix(f.m.status, "MongoDB Compass started with role [one]") {
					t.Fatalf("after launch: review %+v status %q", f.m.review, f.m.status)
				}
			})
		}
	}
}

func TestLaunchKeyRefusesWithoutLiveIAMRun(t *testing.T) {
	const noRun = "No connected DocumentDB IAM run here. Connect the bookmark in Databases (Enter), then press g."
	const notIAM = "g starts DataGrip or Compass for DocumentDB IAM connections only."
	f, ls, _ := setupLaunch(t, dataGripLocal)
	tunnel := docdbBookmark(f)
	tunnel.ID = "tunnel"
	postgres := docdbBookmark(f)
	postgres.ID, postgres.Endpoint, postgres.DBPort, postgres.AuthMode, postgres.Database, postgres.DBUser = "pg", "db.example.rds.amazonaws.com", 5432, "postgres-iam", "app", "reader"
	idle := docdbIAMBookmark(f)
	idle.ID, idle.Database = "idle", "other_db"
	for name, c := range map[string]domain.Connection{"tunnel": tunnel, "postgres-iam": postgres, "idle docdb-iam": idle} {
		want := notIAM
		if c.DocDBIAM() {
			want = noRun
		}
		f.m.review = nil
		f.m.connections = []domain.Connection{c}
		f.m.screen, f.m.selectedRow = databases, 0
		if hasControl(f.m, "Open client", "g") {
			t.Fatalf("%s: offers Open client", name)
		}
		for _, inReview := range []bool{false, true} {
			if inReview {
				key(f.m, "v")
			}
			f.m.status = ""
			key(f.m, "g")
			if f.m.status != want || ls.lookups != 0 || (f.m.review != nil && f.m.review.kind == "client-app") {
				t.Fatalf("%s review=%v: status %q lookups %d", name, inReview, f.m.status, ls.lookups)
			}
		}
	}
	// An ended run of the right kind is refused too.
	f.m.review = nil
	ended := iamRun(f)
	ended.State = "Ended"
	f.sessions.runs = []domain.Run{ended}
	finish(f.m, f.m.load())
	f.m.screen, f.m.selectedRow = tunnels, 0
	key(f.m, "g")
	if f.m.status != noRun || ls.lookups != 0 {
		t.Fatalf("ended run: status %q lookups %d", f.m.status, ls.lookups)
	}
	// A sessions service without the launcher says so.
	f2, _, _ := setupLaunch(t, dataGripLocal)
	f2.m.services.Sessions = f2.sessions
	key(f2.m, "g")
	if f2.m.status != "Starting DataGrip or Compass is not available in this build." {
		t.Fatalf("no launcher: %q", f2.m.status)
	}
}

func TestLaunchPickerWithNoClientOffersTerminal(t *testing.T) {
	f, ls, run := setupLaunch(t)
	f.m.services.Terminals = &testTerminals{frames: map[string]string{run.ID: "app_db> "}}
	key(f.m, "g")
	content := strings.Join(f.m.reviewContent(), "\n")
	if f.m.review == nil || !strings.Contains(content, "No DataGrip or Compass found. Install one, or use the terminal (Enter).") {
		t.Fatalf("none found:\n%s", content)
	}
	key(f.m, "d")
	if len(ls.launched) != 0 || f.m.status != "DataGrip was not found." {
		t.Fatalf("d with nothing found: %+v %q", ls.launched, f.m.status)
	}
	key(f.m, "enter")
	if f.m.terminalID != run.ID || f.m.review != nil {
		t.Fatalf("Enter did not focus the terminal: %q review %+v", f.m.terminalID, f.m.review)
	}
}

func TestClientPlaceAskedOnceUnderWSL(t *testing.T) {
	f, ls, run := setupLaunch(t, dataGripLocal, dataGripWindows, compassLocal)
	oldEnv := getenv
	getenv = func(k string) string {
		if k == "WSL_DISTRO_NAME" {
			return "Example"
		}
		return oldEnv(k)
	}
	t.Cleanup(func() { getenv = oldEnv })
	var saved []string
	f.m.services.SaveClientPlace = func(app, place string) error { saved = append(saved, app+"="+place); return nil }

	key(f.m, "g")
	key(f.m, "d")
	if f.m.review == nil || f.m.review.kind != "client-place" || len(ls.launched) != 0 {
		t.Fatalf("place not asked: review %+v launched %+v", f.m.review, ls.launched)
	}
	if !hasControl(f.m, "Windows", "w") || !hasControl(f.m, "WSL", "l") {
		t.Fatalf("place controls %+v", f.m.controls())
	}
	clickHit(f.m, mouseHit(t, f.m, "key", "w", 0))
	if len(ls.launched) != 1 || ls.launched[0] != (launchCall{run.ID, dataGripWindows}) || strings.Join(saved, ",") != "datagrip=windows" {
		t.Fatalf("launched %+v saved %q", ls.launched, saved)
	}
	// The saved choice skips the prompt.
	key(f.m, "g")
	key(f.m, "d")
	if len(ls.launched) != 2 || ls.launched[1].in != dataGripWindows || len(saved) != 1 {
		t.Fatalf("asked again: launched %+v saved %q review %+v", ls.launched, saved, f.m.review)
	}
	// Compass is found in one place only: never asked.
	key(f.m, "g")
	key(f.m, "m")
	if len(ls.launched) != 3 || ls.launched[2].in != compassLocal || len(saved) != 1 {
		t.Fatalf("compass: launched %+v saved %q", ls.launched, saved)
	}

	// A choice saved by an earlier start is used from the services.
	f2, ls2, _ := setupLaunch(t, dataGripLocal, dataGripWindows)
	f2.m.services.SaveClientPlace = func(app, place string) error { t.Fatal("saved again"); return nil }
	f2.m.clientPlaces = map[string]string{"datagrip": "local"}
	key(f2.m, "g")
	key(f2.m, "d")
	if len(ls2.launched) != 1 || ls2.launched[0].in != dataGripLocal {
		t.Fatalf("saved local not used: %+v review %+v", ls2.launched, f2.m.review)
	}
	if New(domain.Services{ClientPlaces: map[string]string{"compass": "windows"}}).(*Model).clientPlaces["compass"] != "windows" {
		t.Fatal("New dropped the saved choices")
	}

	// A saved place that is no longer found falls back to the found one.
	f3, ls3, _ := setupLaunch(t, dataGripLocal)
	f3.m.clientPlaces = map[string]string{"datagrip": "windows"}
	key(f3.m, "g")
	key(f3.m, "d")
	if len(ls3.launched) != 1 || ls3.launched[0].in != dataGripLocal {
		t.Fatalf("fallback: %+v review %+v", ls3.launched, f3.m.review)
	}

	// Outside WSL, two places never prompt.
	getenv = oldEnv
	f4, ls4, _ := setupLaunch(t, dataGripLocal, dataGripWindows)
	key(f4.m, "g")
	key(f4.m, "d")
	if len(ls4.launched) != 1 || ls4.launched[0].in != dataGripLocal {
		t.Fatalf("non-WSL: %+v review %+v", ls4.launched, f4.m.review)
	}
}

func TestLaunchStatusShowsExpiry(t *testing.T) {
	f, ls, _ := setupLaunch(t, dataGripLocal, compassLocal)
	until := ls.expiry.Local().Format("15:04")
	cmd := press(f.m, "g")
	if f.m.status != "Looking for database clients..." {
		t.Fatalf("lookup status %q", f.m.status)
	}
	finish(f.m, cmd)
	cmd = press(f.m, "d")
	if f.m.status != "Starting DataGrip..." {
		t.Fatalf("start status %q", f.m.status)
	}
	finish(f.m, cmd)
	if want := "DataGrip started with role [one] credentials until " + until + ". It keeps them until it exits."; f.m.status != want {
		t.Fatalf("status %q, want %q", f.m.status, want)
	}
	key(f.m, "g")
	key(f.m, "m")
	if want := "MongoDB Compass started with role [one] credentials until " + until + ". It keeps them until it exits."; f.m.status != want {
		t.Fatalf("status %q, want %q", f.m.status, want)
	}
	// A launch failure shows the fixed text as is.
	ls.err = errors.New("Close DataGrip, then press g again.")
	key(f.m, "g")
	key(f.m, "d")
	if f.m.status != "Close DataGrip, then press g again." {
		t.Fatalf("failure status %q", f.m.status)
	}
	// A failed save still starts the client and says so in fixed text.
	ls.err = nil
	ls.installs = []launch.Install{dataGripLocal, dataGripWindows}
	t.Setenv("WSL_DISTRO_NAME", "Example")
	f.m.services.SaveClientPlace = func(string, string) error { return errors.New("disk full at /secret/path") }
	key(f.m, "g")
	key(f.m, "d")
	key(f.m, "l")
	if n := len(ls.launched); n != 4 || ls.launched[n-1].in != dataGripLocal {
		t.Fatalf("save failure blocked the launch: %+v", ls.launched)
	}
	if want := fmt.Sprintf("DataGrip started with role [one] credentials until %s. It keeps them until it exits. Could not save the Windows or WSL choice; g asks again after a restart.", until); f.m.status != want {
		t.Fatalf("save failure status %q", f.m.status)
	}
	// A second g while a start is still running is refused.
	start := press(f.m, "g")
	if f.m.status != "Looking for database clients..." {
		t.Fatalf("status %q", f.m.status)
	}
	key(f.m, "g")
	if f.m.status != "A database client lookup or start is still running." {
		t.Fatalf("busy status %q", f.m.status)
	}
	finish(f.m, start)
}
