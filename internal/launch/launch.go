// Package launch finds a database client app (DataGrip or MongoDB Compass),
// checks whether DataGrip is already open, and starts the client detached
// with a run's role credentials in its environment only.
package launch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// App is a database client easy-sso can start.
type App string

const (
	DataGrip App = "datagrip"
	Compass  App = "compass"
)

// Place says where an install lives. Windows exists only under WSL.
type Place string

const (
	Local   Place = "local"
	Windows Place = "windows"
)

// Install is one client found on this machine. Path is a POSIX path (a /mnt/...
// path for a Windows install).
type Install struct {
	App   App
	Place Place
	Path  string
}

// Request starts a client. Args never hold secrets (Compass gets the IAM URI);
// Secret holds the KEY=VALUE pairs from awscli.ChildCredentialEnv.
type Request struct {
	Install Install
	Args    []string
	Secret  []string
}

// Launcher finds, checks and starts client apps.
type Launcher interface {
	Find(ctx context.Context, app App) ([]Install, error)
	Running(ctx context.Context, in Install) (bool, error)
	Start(ctx context.Context, r Request) error
}

// Spec is the process build produces.
type Spec struct {
	Path, Dir string
	Args, Env []string
}

// System is the Launcher for this machine.
type System struct{}

// New returns the Launcher for this machine.
func New() Launcher { return System{} }

// Seams for tests.
var (
	goos          = runtime.GOOS
	getenv        = os.Getenv
	fsRoot        = "" // prefix for every file the package reads
	lookPath      = exec.LookPath
	startDetached = func(c *exec.Cmd) error { return c.Start() }
	handoverWait  = 3 * time.Second
)

// Fixed failure texts.
var (
	ErrNativeWindows  = errors.New("DocumentDB IAM login needs Linux, macOS or WSL")
	ErrRunningUnknown = errors.New("easy-sso could not check whether DataGrip is open. Close it, then press g again.")
	ErrHandedOver     = errors.New("DataGrip handed over to a copy that is already open. Close it, then press g again.")
	ErrClientStopped  = errors.New("DataGrip stopped right after it started. Open it once on its own, then press g again.")
	ErrClientStart    = errors.New("The database client could not start.")
	errBadRequest     = errors.New("invalid database client launch request")
	errNullDevice     = errors.New("the AWS null device does not match the database client")
)

// windowsWSLENV lists the variables WSLENV carries to a Windows client.
const windowsWSLENV = "AWS_ACCESS_KEY_ID/w:AWS_SECRET_ACCESS_KEY/w:AWS_SESSION_TOKEN/w:AWS_REGION/w:AWS_CONFIG_FILE/w:AWS_SHARED_CREDENTIALS_FILE/w:AWS_EC2_METADATA_DISABLED/w:AWS_PROFILE/w:AWS_DEFAULT_PROFILE/w"

var mntPath = regexp.MustCompile(`^/mnt/[a-z]/`)

// cleanPath reports whether p is an absolute, clean POSIX path with no
// control characters.
func cleanPath(p string) bool {
	return path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\r\n\x00")
}

// windowsPath reports whether p is a usable /mnt/<drive>/ path, as wslpath
// output is not validated upstream.
func windowsPath(p string) bool { return cleanPath(p) && mntPath.MatchString(p) }

func hasExt(p string, exts ...string) bool {
	lower := strings.ToLower(p)
	for _, e := range exts {
		if strings.HasSuffix(lower, e) {
			return true
		}
	}
	return false
}

// build is pure: it turns a request into the process to start.
func build(goos string, wsl bool, r Request, base []string) (Spec, error) {
	if goos == "windows" {
		return Spec{}, ErrNativeWindows
	}
	in := r.Install
	if (in.App != DataGrip && in.App != Compass) || !cleanPath(in.Path) {
		return Spec{}, errBadRequest
	}
	null := "/dev/null"
	switch in.Place {
	case Local:
		if hasExt(in.Path, ".exe", ".cmd", ".bat") {
			return Spec{}, errBadRequest
		}
	case Windows:
		if goos != "linux" || !wsl || !windowsPath(in.Path) || !hasExt(in.Path, ".exe") {
			return Spec{}, errBadRequest
		}
		null = "NUL"
	default:
		return Spec{}, errBadRequest
	}

	var secrets []string
	nullSeen := 0
	for _, kv := range r.Secret {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, "AWS_") || strings.ContainsAny(kv, "\r\n\x00") {
			return Spec{}, errBadRequest
		}
		switch key {
		case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN":
			if val != "" {
				secrets = append(secrets, val)
			}
		case "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE":
			if val != null {
				return Spec{}, errNullDevice
			}
			nullSeen++
		}
	}
	if nullSeen != 2 {
		return Spec{}, errNullDevice
	}
	for _, a := range r.Args {
		for _, s := range secrets {
			if strings.Contains(a, s) {
				return Spec{}, errBadRequest
			}
		}
	}

	env := make([]string, 0, len(base)+len(r.Secret)+3)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "AWS_") || key == "ELECTRON_RUN_AS_NODE" || key == "WSLENV" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, r.Secret...)
	spec := Spec{Path: in.Path, Args: append([]string(nil), r.Args...)}
	if in.Place == Windows {
		// A fresh WSLENV: the user's own list never reaches the client. The
		// empty profiles become undefined on the Windows side.
		env = append(env, "AWS_PROFILE=", "AWS_DEFAULT_PROFILE=", "WSLENV="+windowsWSLENV)
		// A /mnt/<d> working folder avoids cmd's UNC-path warning.
		spec.Dir = path.Dir(in.Path)
	}
	spec.Env = env
	return spec, nil
}
