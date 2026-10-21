package sessions

import (
	"context"
	"errors"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/database"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/launch"
)

// hostOS is a seam: tests model native Windows.
var hostOS = runtime.GOOS

// clientMinLife is the least credential life a launched client gets: it
// keeps the keys until it exits and cannot be renewed in place.
const clientMinLife = 10 * time.Minute

// Fixed launch refusals.
var (
	errClientNotIAM     = errors.New("Only a DocumentDB IAM run can start a database client")
	errClientNotOpen    = errors.New("The DocumentDB IAM run is not connected; connect it from Databases before launching a client")
	errClientNotOwned   = errors.New("This run was not opened by this easy-sso; open the connection again from Databases before launching a client")
	errClientRole       = errors.New("Your role was removed or unpublished; log in again before launching a client")
	errDataGripOpen     = errors.New("Close DataGrip, then press g again.")
	errClientCA         = errors.New("The cached CA file is missing or invalid; connect again from Databases before launching a client")
	errClientCAWindows  = errors.New("The CA file could not be copied to Windows; check WSL interop, then press g again")
	errClientBadInstall = errors.New("Unknown database client")
)

// ClientApps lists the DataGrip installs, then the Compass installs. A
// failed lookup only hides that app.
func (m *Manager) ClientApps(ctx context.Context) ([]launch.Install, error) {
	var out []launch.Install
	for _, app := range []launch.App{launch.DataGrip, launch.Compass} {
		found, err := m.options.Launcher.Find(ctx, app)
		if err != nil {
			continue
		}
		out = append(out, found...)
	}
	return out, nil
}

// LaunchClientApp starts a database client with this run's frozen role
// credentials in its environment. The client is not owned: it keeps the
// credentials until it exits or they expire.
//
// It returns the credentials' expiry for the status line. Starting DataGrip
// blocks for up to about 3 s while easy-sso watches for a handover to a copy
// that is already open, so call it off the UI goroutine.
func (m *Manager) LaunchClientApp(ctx context.Context, runID string, in launch.Install) (time.Time, error) {
	if hostOS == "windows" {
		return time.Time{}, launch.ErrNativeWindows
	}
	if in.App != launch.DataGrip && in.App != launch.Compass {
		return time.Time{}, errClientBadInstall
	}
	en, err := m.find(runID)
	if err != nil {
		return time.Time{}, err
	}
	// The run's own frozen snapshot, never the Credentials view's current one.
	en.mu.Lock()
	run, snapshot, owned, recovered, verified := copyRun(en.run), en.snapshot, en.child != nil, en.recovered, en.verified
	en.mu.Unlock()
	c := run.Connection
	if c.Kind != "db" || !c.DocDBIAM() {
		return time.Time{}, errClientNotIAM
	}
	// The client skips the hostname check on 127.0.0.1; only a run whose
	// TLS check passed makes up for that.
	if (run.State != "Listening" && run.State != "Connected") || !verified {
		return time.Time{}, errClientNotOpen
	}
	if !owned || recovered || snapshot.AccessKeyID == "" {
		return time.Time{}, errClientNotOwned
	}
	// The frozen snapshot's expiry comes first: a role that is nearly
	// expired also fails the Credentials check below, with the wrong reason.
	if time.Until(snapshot.Expiration) < clientMinLife {
		return time.Time{}, clientExpiry(snapshot)
	}
	// A removed or unpublished role starts no new client, even from its own
	// frozen snapshot.
	if m.options.RecoveryCredentials == nil {
		return time.Time{}, errClientRole
	}
	if _, _, err := m.options.RecoveryCredentials(run.Alias); err != nil {
		return time.Time{}, errClientRole
	}
	null := os.DevNull
	if in.Place == launch.Windows {
		null = "NUL"
	}
	secret, err := awscli.ChildCredentialEnv(snapshot, c.Region, null, clientMinLife)
	if err != nil {
		if time.Until(snapshot.Expiration) < clientMinLife {
			return time.Time{}, clientExpiry(snapshot)
		}
		return time.Time{}, err
	}
	running, err := m.options.Launcher.Running(ctx, in)
	if err != nil {
		return time.Time{}, launch.ErrRunningUnknown
	}
	if running {
		return time.Time{}, errDataGripOpen
	}
	var args []string
	if in.App == launch.Compass {
		ca, err := m.clientCA(ctx, c, in.Place)
		if err != nil {
			return time.Time{}, err
		}
		args = []string{compassURI(c.LocalPort, c.Database, ca)}
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	// Start may wait about 3 s for a DataGrip handover; a caller's deadline
	// must not cut that wait short, or a handover would read as success.
	start, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := m.options.Launcher.Start(start, launch.Request{Install: in, Args: args, Secret: secret}); err != nil {
		return time.Time{}, err
	}
	return snapshot.Expiration, nil
}

// clientExpiry is the fixed refusal for credentials that expire too soon.
func clientExpiry(s domain.Snapshot) error {
	return errors.New("Credentials expire at " + s.Expiration.Local().Format("15:04") + "; renew the role and reconnect before launching a client")
}

// clientCA returns the CA path the client reads: the cached bundle, or for a
// Windows client its refreshed copy under %USERPROFILE%\.easy-sso.
func (m *Manager) clientCA(ctx context.Context, c domain.Connection, place launch.Place) (string, error) {
	ca, err := database.CAPath(c, m.stateDir)
	if err != nil || !database.CachedCA(ca) {
		return "", errClientCA
	}
	if place != launch.Windows {
		return ca, nil
	}
	exp, err := m.exportCA(ctx, ca, false)
	if err != nil || !exp.Exists || exp.Windows == "" || strings.ContainsAny(exp.Windows, "\r\n\x00") {
		return "", errClientCAWindows
	}
	return exp.Windows, nil
}

// compassURI is the IAM connection string for Compass. It names no user and
// carries no key: Compass reads the role keys from its environment.
func compassURI(localPort int, db, ca string) string {
	escapedCA := strings.ReplaceAll(url.QueryEscape(ca), "+", "%20")
	return "mongodb://127.0.0.1:" + strconv.Itoa(localPort) + "/" + url.PathEscape(db) +
		"?tls=true&tlsCAFile=" + escapedCA +
		"&directConnection=true&retryWrites=false&tlsAllowInvalidHostnames=true&authSource=%24external&authMechanism=MONGODB-AWS"
}
