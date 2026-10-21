package database

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/store"
)

// mongoshFloor is the first mongosh release whose bundled AWS SDK is not
// exposed to the XML entity expansion flaw in MONGODB-AWS authentication.
var mongoshFloor = [3]int{2, 8, 1}

// mongoshConfig turns off telemetry, logging, history, update checks and
// snippet downloads. It must stay valid JSON: mongosh ignores an invalid file
// without a warning and falls back to defaults that write and fetch data.
const mongoshConfig = `{"enableTelemetry":false,"disableLogging":true,"historyLength":0,"updateURL":"","snippetAutoload":false}`

// tunnelFailure names a local listener that did not reach the cluster.
const tunnelFailure = "The tunnel did not answer; check the relay"

// mongoHomeMaxAge is how long a leftover private HOME is kept when no live run owns it.
const mongoHomeMaxAge = 24 * time.Hour

var (
	mongoVersionRE = regexp.MustCompile(`^\s*(\d+)\.(\d+)\.(\d+)`)
	mongoRunIDRE   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	// mongoDatabaseRE is the connections rule, checked again before the URI is built.
	mongoDatabaseRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)
)

// PrepareMongo resolves mongosh, enforces the version floor and readies the CA
// bundle before an SSM session is opened.
func PrepareMongo(ctx context.Context, c domain.Connection, stateDir string) (string, string, error) {
	path, err := exec.LookPath("mongosh")
	if err != nil {
		return "", "", errors.New("MongoDB shell missing: install mongosh 2.8.1 or later, then retry")
	}
	version, err := mongoshVersion(ctx, path, stateDir)
	if err != nil {
		return "", "", err
	}
	if !atLeast(version, mongoshFloor) {
		return "", "", errors.New("mongosh 2.8.1 or later is required for IAM login")
	}
	ca, err := EnsureCA(ctx, c, stateDir)
	if err != nil {
		return "", "", err
	}
	return path, ca, nil
}

// mongoshVersion runs `mongosh --version` with only PATH and a throwaway HOME,
// so no ambient credential, Node option or user config reaches it.
func mongoshVersion(ctx context.Context, path, stateDir string) ([3]int, error) {
	home, cleanup, err := MongoHome(stateDir, "version")
	if err != nil {
		return [3]int{}, err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err = cmd.Run(); err != nil {
		return [3]int{}, errors.New("mongosh did not report its version; reinstall mongosh 2.8.1 or later, then retry")
	}
	m := mongoVersionRE.FindStringSubmatch(out.String())
	if m == nil {
		return [3]int{}, errors.New("mongosh 2.8.1 or later is required for IAM login")
	}
	var v [3]int
	for i := range v {
		if v[i], err = strconv.Atoi(m[i+1]); err != nil {
			return [3]int{}, errors.New("mongosh 2.8.1 or later is required for IAM login")
		}
	}
	return v, nil
}

func atLeast(v, floor [3]int) bool {
	for i := range v {
		if v[i] != floor[i] {
			return v[i] > floor[i]
		}
	}
	return true
}

// MongoHome creates a private HOME for one mongosh run, so its config, history
// and logs never land in the user's real home. cleanup removes it.
func MongoHome(stateDir, id string) (string, func(), error) {
	base := filepath.Join(stateDir, "mongosh")
	if err := store.EnsureDir(base); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(base, id+"-")
	if err != nil {
		return "", nil, errors.New("could not create a private home for mongosh")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err = store.AtomicWrite(filepath.Join(dir, ".mongodb", "mongosh", "config"), []byte(mongoshConfig)); err != nil {
		cleanup()
		return "", nil, errors.New("could not create a private home for mongosh")
	}
	return dir, cleanup, nil
}

// SweepMongoHomes removes private HOMEs left by a crash. It keeps a directory
// that is younger than a day or that a live run owns, and never follows a link.
func SweepMongoHomes(stateDir string, owned func(runID string) bool) {
	base := filepath.Join(stateDir, "mongosh")
	if store.CheckPath(base) != nil {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < mongoHomeMaxAge {
			continue
		}
		name := entry.Name()
		cut := strings.LastIndex(name, "-")
		if cut <= 0 || owned(name[:cut]) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(base, name))
	}
}

// MongoCommand builds a mongosh child for MONGODB-AWS. The frozen credentials
// go only into its environment: the driver refuses keys in the URI, and argv
// is readable by other local users. The local listener does not carry the
// cluster hostname, so VerifyTLS checks the hostname before launch.
func MongoCommand(path, ca, home, id string, s domain.Snapshot, c domain.Connection) (*exec.Cmd, *Output, error) {
	if !mongoRunIDRE.MatchString(id) {
		return nil, nil, errors.New("invalid run id for the database terminal")
	}
	// An @, / or ? in the name could re-split the URI below.
	if c.Database != "" && !mongoDatabaseRE.MatchString(c.Database) {
		return nil, nil, errors.New("DocumentDB database name may use only letters, digits, _ and -")
	}
	creds, err := awscli.ChildCredentialEnv(s, c.Region, os.DevNull, 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	marker := "\x1b]777;easy-sso-db-ready-" + id + "\x07"
	uri := "mongodb://127.0.0.1:" + strconv.Itoa(c.LocalPort) + "/" + url.PathEscape(c.Database) + "?directConnection=true&retryWrites=false&authSource=%24external&authMechanism=MONGODB-AWS&serverSelectionTimeoutMS=15000&connectTimeoutMS=15000"
	// The trailing undefined stops mongosh from echoing the assigned function.
	prompt := "prompt = () => '" + marker + "' + db.getName() + '> '; undefined"
	cmd := exec.Command(path, "--quiet", "--tls", "--tlsCAFile", ca, "--tlsAllowInvalidHostnames", uri, "--shell", "--eval", prompt)
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		switch key {
		case "PATH", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR":
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Env = append(cmd.Env, creds...)
	output := &Output{marker: marker, secrets: []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken}, classify: mongoFailure, fallback: "mongosh did not reach an authenticated prompt; check the relay and the $external user", Ready: make(chan struct{})}
	return cmd, output, nil
}

// mongoFailure matches mongosh error text. A PTY mixes stdout and stderr, so
// only the text is used.
func mongoFailure(d string) string {
	switch {
	case strings.Contains(d, "authentication failed"):
		return "DocumentDB refused the IAM login; ask the admin to map your role ARN in $external (see v)"
	case strings.Contains(d, "certificate"):
		return tlsFailure
	case strings.Contains(d, "mongoserverselectionerror"), strings.Contains(d, "server selection"), strings.Contains(d, "econnrefused"):
		return tunnelFailure
	}
	return ""
}

// VerifyTLS checks the cluster certificate and hostname through the local
// tunnel. It makes up for --tlsAllowInvalidHostnames, which mongosh needs
// because it connects to 127.0.0.1.
func VerifyTLS(ctx context.Context, port int, endpoint, ca string) error {
	data, err := readCA(ca)
	pool := x509.NewCertPool()
	if err != nil || !pool.AppendCertsFromPEM(data) {
		return errors.New(caInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return errors.New(tunnelFailure)
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: endpoint, RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err = conn.HandshakeContext(ctx); err != nil {
		if certificateRejected(err) {
			return errors.New(tlsFailure)
		}
		// SSM port forwarding accepts locally, then drops the connection
		// (EOF or reset) when the relay cannot reach the cluster.
		return errors.New(tunnelFailure)
	}
	_ = conn.Close()
	return nil
}

// certificateRejected reports whether the handshake failed on the server
// certificate itself, not on the connection.
func certificateRejected(err error) bool {
	var verify *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verify) || errors.As(err, &hostname) || errors.As(err, &authority) || errors.As(err, &invalid)
}
