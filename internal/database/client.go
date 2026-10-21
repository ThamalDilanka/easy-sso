// Package database prepares an isolated PostgreSQL IAM client. It never persists
// authentication material or executes SQL as part of connection setup.
package database

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/store"
)

// Prepare resolves prerequisites before an SSM session is opened. An explicit
// CA file supports offline use; otherwise only the official AWS bundle is used.
func Prepare(ctx context.Context, c domain.Connection, stateDir string) (string, string, error) {
	path, err := exec.LookPath("psql")
	if err != nil {
		return "", "", errors.New("PostgreSQL client missing: install psql, then retry Connect terminal")
	}
	ca, err := EnsureCA(ctx, c, stateDir)
	if err != nil {
		return "", "", err
	}
	return path, ca, nil
}

// caURLs are the official public AWS bundles. China has none, so it needs an
// explicit CA file.
var caURLs = map[string]string{
	"aws":        "https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem",
	"aws-us-gov": "https://truststore.pki.us-gov-west-1.rds.amazonaws.com/global/global-bundle.pem",
}

var caClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}

// CAPath names the CA bundle a connection uses, without any I/O.
func CAPath(c domain.Connection, stateDir string) (string, error) {
	if c.CAFile != "" {
		ca, err := filepath.Abs(c.CAFile)
		if err != nil {
			return "", errors.New("invalid RDS CA file path")
		}
		return ca, nil
	}
	if caURLs[c.Partition] == "" {
		return "", errors.New("set the RDS CA file for this AWS partition before connecting")
	}
	return filepath.Join(stateDir, "rds-ca-"+c.Partition+".pem"), nil
}

// EnsureCA returns a valid CA bundle path. An explicit file is only checked;
// the public AWS bundle is reused for 24h, then downloaded and cached.
func EnsureCA(ctx context.Context, c domain.Connection, stateDir string) (string, error) {
	ca, err := CAPath(c, stateDir)
	if err != nil {
		return "", err
	}
	if c.CAFile != "" {
		data, e := os.ReadFile(ca)
		if e != nil || !validCA(data) {
			return "", errors.New(caInvalid)
		}
		return ca, nil
	}
	if info, e := os.Stat(ca); e == nil && time.Since(info.ModTime()) < 24*time.Hour {
		if data, e := os.ReadFile(ca); e == nil && validCA(data) {
			return ca, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, caURLs[c.Partition], nil)
	if err != nil {
		return "", err
	}
	res, err := caClient.Do(req)
	if err != nil {
		return "", errors.New("could not download the AWS RDS CA bundle; check network access or set a local CA file")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1024*1024+1))
	if err != nil || res.StatusCode != http.StatusOK || len(data) > 1024*1024 || !validCA(data) {
		return "", errors.New("AWS RDS CA bundle download was invalid")
	}
	if err = store.AtomicWrite(ca, data); err != nil {
		return "", errors.New("could not cache the public RDS CA bundle")
	}
	return ca, nil
}

func validCA(data []byte) bool { return x509.NewCertPool().AppendCertsFromPEM(data) }

// Command uses a child-only environment for the password. No token, AWS
// credential, or connection string containing a password appears in argv.
func Command(path, ca, id, token string, c domain.Connection) (*exec.Cmd, *Output) {
	marker := "\x1b]777;easy-sso-db-ready-" + id + "\x07"
	output := &Output{marker: marker, secrets: []string{token}, classify: postgresFailure, fallback: "PostgreSQL did not reach an authenticated prompt; check terminal output, database reachability and IAM setup.", Ready: make(chan struct{})}
	cmd := exec.Command(path, "-X", "-w", "--set", "PROMPT1=%["+marker+"%]%/%R%x%# ", "--set", "HISTFILE="+os.DevNull, "--pset", "pager=off")
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		switch key {
		case "PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "SystemRoot", "WINDIR":
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "COLORTERM=truecolor", "PGHOST="+c.Endpoint, "PGHOSTADDR=127.0.0.1", "PGPORT="+strconv.Itoa(c.LocalPort), "PGDATABASE="+c.Database, "PGUSER="+c.DBUser, "PGPASSWORD="+token, "PGSSLMODE=verify-full", "PGSSLROOTCERT="+ca, "PGCONNECT_TIMEOUT=15", "PGGSSENCMODE=disable", "PGAPPNAME=easy-sso", "PSQL_HISTORY="+os.DevNull)
	return cmd, output
}

// tlsFailure is shared by every engine: the CA bundle and the real endpoint are
// the same inputs whichever client verifies them.
const tlsFailure = "Database TLS verification failed; check the real database endpoint and its CA bundle."

// caInvalid is shared by every engine; the field is named "CA file" on the
// DocumentDB form and "RDS CA file" on the PostgreSQL one.
const caInvalid = "CA file unavailable or invalid; select a PEM certificate bundle"

// Output recognizes the first interactive prompt, emitted only after login.
// The private nonprinting prompt marker is removed before terminal rendering.
// It also redacts every secret even across read boundaries, without a transcript.
// Failures are classified on text only and always reported as fixed text.
type Output struct {
	mu                          sync.Mutex
	marker, pending, diagnostic string
	secrets                     []string
	classify                    func(string) string
	fallback                    string
	ready                       bool
	Ready                       chan struct{}
	failure                     string
}

func (o *Output) Filter(p []byte) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pending += string(p)
	var out strings.Builder
	for len(o.pending) > 0 {
		if strings.HasPrefix(o.pending, o.marker) {
			o.pending = o.pending[len(o.marker):]
			if !o.ready {
				o.ready = true
				close(o.Ready)
			}
			continue
		}
		if secret := o.secretAt(); secret != "" {
			out.WriteString("[redacted]")
			o.pending = o.pending[len(secret):]
			continue
		}
		if o.partial() {
			break
		}
		out.WriteByte(o.pending[0])
		o.pending = o.pending[1:]
	}
	text := out.String()
	if !o.ready {
		d := strings.ToLower(o.diagnostic + text)
		if failure := o.classify(d); failure != "" {
			o.failure = failure
		}
		if len(d) > 256 {
			d = d[len(d)-256:]
		}
		o.diagnostic = d
	}
	return []byte(text)
}

// secretAt returns the longest secret that starts the pending text.
func (o *Output) secretAt() string {
	match := ""
	for _, secret := range o.secrets {
		if secret != "" && len(secret) > len(match) && strings.HasPrefix(o.pending, secret) {
			match = secret
		}
	}
	return match
}

// partial reports whether the pending text may still grow into the marker or a secret.
func (o *Output) partial() bool {
	if strings.HasPrefix(o.marker, o.pending) {
		return true
	}
	for _, secret := range o.secrets {
		if secret != "" && strings.HasPrefix(secret, o.pending) {
			return true
		}
	}
	return false
}

func postgresFailure(d string) string {
	switch {
	case strings.Contains(d, "certificate"), strings.Contains(d, "ssl error"):
		return tlsFailure
	case strings.Contains(d, "authentication failed"), strings.Contains(d, "no pg_hba.conf"), strings.Contains(d, "password authentication"):
		return "Database IAM login rejected. Ask an administrator to check RDS IAM is enabled, this DB user has rds_iam, and the selected role has rds-db:connect for the DB resource ID and username. While setup is pending, Edit (e) and set Auth to tunnel to use an existing database login in your own client."
	case strings.Contains(d, "does not exist"):
		return "Database login failed; verify the saved database name and database username."
	}
	return ""
}

func (o *Output) Failure() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.failure != "" {
		return o.failure
	}
	return o.fallback
}

// CachedCA reports whether path holds a valid CA bundle, whatever its age.
// It only labels a still-usable file; EnsureCA never falls back to it.
func CachedCA(path string) bool {
	data, err := readCA(path)
	return err == nil && validCA(data)
}

// readCA reads public CA content the way EnsureCA accepts it: links in the
// path are fine, but only a regular file up to 1 MiB is read.
func readCA(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, e := f.Stat(); e != nil || !st.Mode().IsRegular() {
		return nil, errors.New("CA file must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err == nil && len(data) > 1024*1024 {
		err = errors.New("CA file exceeds 1 MiB")
	}
	return data, err
}
