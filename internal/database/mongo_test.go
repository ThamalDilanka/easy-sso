package database

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

func mongoSnapshot() domain.Snapshot {
	return domain.Snapshot{AccessKeyID: "ASIATESTKEY", SecretAccessKey: "TOP-SECRET-VALUE", SessionToken: "SESSION-TOKEN-VALUE", Expiration: time.Now().Add(time.Hour)}
}

func docdbConnection() domain.Connection {
	return domain.Connection{Kind: "db", AuthMode: "docdb-iam", Endpoint: "cluster.example.invalid", Region: "us-east-1", DBPort: 27017, LocalPort: 27018, Database: "exampledb"}
}

// testCA writes a self-signed CA certificate for host and returns its key pair.
func testCA(t *testing.T, host string) (string, tls.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return ca, pair
}

func TestMongoCommandIsolationAndRedaction(t *testing.T) {
	for _, key := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "NODE_OPTIONS", "NODE_PATH", "NODE_EXTRA_CA_CERTS", "NODE_TLS_REJECT_UNAUTHORIZED", "MONGOSH_GLOBAL_CONFIG_FILE_FOR_TESTING", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "HOME", "TERM"} {
		t.Setenv(key, "ambient-secret")
	}
	allowed := map[string]string{"PATH": "/usr/bin", "USER": "tester", "LOGNAME": "tester", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TMPDIR": "/tmp"}
	for key, value := range allowed {
		t.Setenv(key, value)
	}
	s, c := mongoSnapshot(), docdbConnection()
	home := filepath.Join(t.TempDir(), "private home")
	cmd, output, err := MongoCommand("/usr/bin/mongosh", "/state dir/ca.pem", home, "test-run", s, c)
	if err != nil {
		t.Fatal(err)
	}
	uri := "mongodb://127.0.0.1:27018/exampledb?directConnection=true&retryWrites=false&authSource=%24external&authMechanism=MONGODB-AWS&serverSelectionTimeoutMS=15000&connectTimeoutMS=15000"
	prompt := "prompt = () => '\x1b]777;easy-sso-db-ready-test-run\x07' + db.getName() + '> '; undefined"
	want := []string{"/usr/bin/mongosh", "--quiet", "--tls", "--tlsCAFile", "/state dir/ca.pem", "--tlsAllowInvalidHostnames", uri, "--shell", "--eval", prompt}
	if strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv %q", cmd.Args)
	}
	args := strings.Join(cmd.Args, " ")
	for _, secret := range []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken} {
		if strings.Contains(args, secret) {
			t.Fatal("credential in argv")
		}
	}
	if strings.Contains(cmd.Args[6], "@") || !strings.Contains(cmd.Args[6], "authSource=%24external") {
		t.Fatal("unsafe URI", cmd.Args[6])
	}
	expected := []string{"HOME=" + home, "TERM=xterm-256color", "COLORTERM=truecolor", "AWS_ACCESS_KEY_ID=ASIATESTKEY", "AWS_SECRET_ACCESS_KEY=TOP-SECRET-VALUE", "AWS_SESSION_TOKEN=SESSION-TOKEN-VALUE", "AWS_REGION=us-east-1", "AWS_CONFIG_FILE=" + os.DevNull, "AWS_SHARED_CREDENTIALS_FILE=" + os.DevNull, "AWS_EC2_METADATA_DISABLED=true"}
	for key, value := range allowed {
		expected = append(expected, key+"="+value)
	}
	got := append([]string(nil), cmd.Env...)
	sort.Strings(got)
	sort.Strings(expected)
	if strings.Join(got, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("child env %q\nwant %q", got, expected)
	}

	blank, _, err := MongoCommand("/usr/bin/mongosh", "/ca.pem", home, "test-run", s, domain.Connection{LocalPort: 27018, Region: "us-east-1"})
	if err != nil || !strings.HasPrefix(blank.Args[6], "mongodb://127.0.0.1:27018/?directConnection") {
		t.Fatal("blank database not omitted", err)
	}
	expired := s
	expired.Expiration = time.Now().Add(10 * time.Second)
	if _, _, err := MongoCommand("/usr/bin/mongosh", "/ca.pem", home, "test-run", expired, c); err == nil {
		t.Fatal("short-lived credentials accepted")
	}
	if _, _, err := MongoCommand("/usr/bin/mongosh", "/ca.pem", home, "x'; evil", s, c); err == nil {
		t.Fatal("unsafe run id accepted")
	}
	// The database name is checked again here, so nothing can re-split the URI.
	for _, db := range []string{"user@evil.example.invalid", "a/b", "db?authSource=admin", "db#x", "db name", "db%40", "db.x", strings.Repeat("d", 64)} {
		bad := c
		bad.Database = db
		if _, _, err := MongoCommand("/usr/bin/mongosh", "/ca.pem", home, "test-run", s, bad); err == nil || err.Error() != "DocumentDB database name may use only letters, digits, _ and -" {
			t.Fatalf("database %q: err = %v", db, err)
		}
	}
	ok := c
	ok.Database = "App_db-1" + strings.Repeat("d", 55)
	if _, _, err := MongoCommand("/usr/bin/mongosh", "/ca.pem", home, "test-run", s, ok); err != nil {
		t.Fatalf("63-character name refused: %v", err)
	}

	var shown strings.Builder
	text := "process.env " + s.AccessKeyID + " " + s.SecretAccessKey + " " + s.SessionToken + "\n" + output.marker + "exampledb> "
	for _, b := range []byte(text) {
		shown.Write(output.Filter([]byte{b}))
	}
	out := shown.String()
	for _, secret := range []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken} {
		if strings.Contains(out, secret) {
			t.Fatal("credential shown", out)
		}
	}
	if strings.Contains(out, "easy-sso-db-ready") || strings.Count(out, "[redacted]") != 3 || !strings.HasSuffix(out, "exampledb> ") {
		t.Fatalf("unsafe output filtering %q", out)
	}
	select {
	case <-output.Ready:
	default:
		t.Fatal("split prompt not detected")
	}
}

func TestMongoLoginFailureText(t *testing.T) {
	const (
		auth    = "DocumentDB refused the IAM login; ask the admin to map your role ARN in $external (see v)"
		tunnel  = "The tunnel did not answer; check the relay"
		tlsText = "Database TLS verification failed; check the real database endpoint and its CA bundle."
		other   = "mongosh did not reach an authenticated prompt; check the relay and the $external user"
	)
	for _, tc := range []struct{ text, want string }{
		{"MongoServerError: Authentication failed.\r\n", auth},
		{"MongoServerSelectionError: Server selection timed out after 15000 ms\r\n", tunnel},
		{"MongoServerSelectionError: Socket 'secureConnect' timed out after 15007ms (connectTimeoutMS: 15000)\r\n", tunnel},
		{"MongoNetworkError: connect ECONNREFUSED 127.0.0.1:27018\r\n", tunnel},
		{"MongoNetworkError: unable to get local issuer certificate\r\n", tlsText},
		{"Current Mongosh Log ID: 0123\r\nsomething unexpected\r\n", other},
	} {
		_, output, err := MongoCommand("mongosh", "/ca.pem", "/home", "test", mongoSnapshot(), docdbConnection())
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range []byte(tc.text) {
			output.Filter([]byte{b})
		}
		select {
		case <-output.Ready:
			t.Fatal("error counted as authenticated")
		default:
		}
		if got := output.Failure(); got != tc.want {
			t.Fatalf("%q: got %q, want %q", tc.text, got, tc.want)
		}
		if strings.ContainsAny(tc.want, "—–") {
			t.Fatal("dash in user-facing text")
		}
	}
	if _, output := Command("psql", "/ca", "test", "secret", domain.Connection{}); !strings.HasPrefix(output.Failure(), "PostgreSQL did not reach") {
		t.Fatal("psql default changed", output.Failure())
	}
}

// fakeMongosh writes an executable that records its environment and prints version.
func fakeMongosh(t *testing.T, version string) (dir, envFile string) {
	t.Helper()
	dir = t.TempDir()
	envFile = filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nenv > '" + envFile + "'\necho '" + version + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "mongosh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return dir, envFile
}

func TestPrepareMongoVersionFloor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake executable uses a POSIX shell")
	}
	for _, key := range []string{"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "NODE_OPTIONS"} {
		t.Setenv(key, "ambient-secret")
	}
	ca, _ := testCA(t, "cluster.example.invalid")
	c := docdbConnection()
	c.CAFile = ca
	basePath := os.Getenv("PATH")

	t.Setenv("PATH", t.TempDir())
	if _, _, err := PrepareMongo(context.Background(), c, t.TempDir()); err == nil || err.Error() != "MongoDB shell missing: install mongosh 2.8.1 or later, then retry" {
		t.Fatal("missing mongosh", err)
	}
	for _, tc := range []struct {
		version string
		ok      bool
	}{{"2.8.0", false}, {"2.8.1", true}, {"2.13.0", true}, {"1.10.6", false}, {"not a version", false}} {
		dir, envFile := fakeMongosh(t, tc.version)
		t.Setenv("PATH", dir+string(os.PathListSeparator)+basePath)
		state := t.TempDir()
		path, gotCA, err := PrepareMongo(context.Background(), c, state)
		if tc.ok != (err == nil) {
			t.Fatalf("%s: %v", tc.version, err)
		}
		if tc.ok && (path != filepath.Join(dir, "mongosh") || gotCA != ca) {
			t.Fatal(path, gotCA)
		}
		if !tc.ok && err.Error() != "mongosh 2.8.1 or later is required for IAM login" {
			t.Fatal(err)
		}
		data, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatal(err)
		}
		env := string(data)
		if strings.Contains(env, "AWS_") || strings.Contains(env, "ambient-secret") || strings.Contains(env, "NODE_OPTIONS") {
			t.Fatal("version child inherited ambient settings", env)
		}
		var home string
		for _, line := range strings.Split(env, "\n") {
			if value, ok := strings.CutPrefix(line, "HOME="); ok {
				home = value
			}
		}
		if home == "" || !strings.HasPrefix(home, state) {
			t.Fatal("version child HOME not private", home)
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("throwaway HOME left behind")
		}
	}
}

func TestMongoHomeSeedAndCleanup(t *testing.T) {
	state := t.TempDir()
	dir, cleanup, err := MongoHome(state, "run-one")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != filepath.Join(state, "mongosh") || !strings.HasPrefix(filepath.Base(dir), "run-one-") {
		t.Fatal("home location", dir)
	}
	config := filepath.Join(dir, ".mongodb", "mongosh", "config")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"enableTelemetry":false,"disableLogging":true,"historyLength":0,"updateURL":"","snippetAutoload":false}` {
		t.Fatalf("config bytes %q", data)
	}
	// mongosh silently ignores an invalid config and then logs, keeps history and downloads snippets.
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed) != 5 || parsed["disableLogging"] != true || parsed["updateURL"] != "" {
		t.Fatal("config is not the intended JSON", parsed, err)
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, ".mongodb"): 0700, filepath.Dir(config): 0700, config: 0600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("%s mode %v, want %v (%v)", path, info.Mode().Perm(), mode, err)
			}
		}
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cleanup kept the private HOME")
	}

	old := time.Now().Add(-25 * time.Hour)
	homes := map[string]string{}
	for _, id := range []string{"0000aaaa-live", "0000bbbb-dead", "0000cccc-fresh"} {
		home, _, err := MongoHome(state, id)
		if err != nil {
			t.Fatal(err)
		}
		homes[id] = home
		if id != "0000cccc-fresh" {
			if err := os.Chtimes(home, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(outside, filepath.Join(state, "mongosh", "linked-1")); err != nil {
			t.Fatal(err)
		}
	}
	SweepMongoHomes(state, func(id string) bool { return id == "0000aaaa-live" })
	if _, err := os.Stat(homes["0000bbbb-dead"]); !os.IsNotExist(err) {
		t.Fatal("sweep kept an old unowned HOME")
	}
	for _, id := range []string{"0000aaaa-live", "0000cccc-fresh"} {
		if _, err := os.Stat(homes[id]); err != nil {
			t.Fatal("sweep removed", id, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("sweep followed a link", err)
	}
	SweepMongoHomes(t.TempDir(), func(string) bool { return false })
}

func TestVerifyTLS(t *testing.T) {
	ca, pair := testCA(t, "cluster.example.invalid")
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	if err := VerifyTLS(context.Background(), port, "cluster.example.invalid", ca); err != nil {
		t.Fatal(err)
	}
	err = VerifyTLS(context.Background(), port, "other.example.invalid", ca)
	if err == nil || err.Error() != "Database TLS verification failed; check the real database endpoint and its CA bundle." {
		t.Fatal("wrong hostname accepted", err)
	}
	// SSM port forwarding always accepts locally, then drops the connection
	// when the relay cannot reach the cluster.
	dropped, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dropped.Close()
	go func() {
		for {
			conn, err := dropped.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	err = VerifyTLS(context.Background(), dropped.Addr().(*net.TCPAddr).Port, "cluster.example.invalid", ca)
	if err == nil || err.Error() != "The tunnel did not answer; check the relay" {
		t.Fatal("dropped tunnel not reported as the tunnel", err)
	}
	if err := VerifyTLS(context.Background(), port, "cluster.example.invalid", filepath.Join(t.TempDir(), "missing.pem")); err == nil || err.Error() != "CA file unavailable or invalid; select a PEM certificate bundle" {
		t.Fatal("missing CA accepted or named for RDS", err)
	}
}

// A real mongosh proves the version parse and that it accepts the argv, without a server.
func TestRealMongoshPromptMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("embedded database terminals do not run on native Windows")
	}
	if _, err := exec.LookPath("mongosh"); err != nil {
		t.Skip("mongosh unavailable")
	}
	ca, _ := testCA(t, "cluster.example.invalid")
	c := docdbConnection()
	c.CAFile = ca
	state := t.TempDir()
	path, gotCA, err := PrepareMongo(context.Background(), c, state)
	if err != nil {
		t.Skip("installed mongosh is below the IAM floor:", err)
	}
	home, cleanup, err := MongoHome(state, "real-run")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd, _, err := MongoCommand(path, gotCA, home, "real-run", mongoSnapshot(), c)
	if err != nil {
		t.Fatal(err)
	}
	// --nodb refuses the TLS options and the URI, so only the shell flags and
	// the prompt program run here; the full argv needs a live cluster.
	args := []string{"--nodb"}
	for i := 1; i < len(cmd.Args); i++ {
		switch arg := cmd.Args[i]; arg {
		case "--quiet":
			args = append(args, arg)
		case "--eval":
			args = append(args, arg, cmd.Args[i+1])
			i++
		}
	}
	if len(args) != 4 {
		t.Fatal("unexpected argv shape", cmd.Args)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, path, args...)
	run.Env = cmd.Env
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("mongosh refused the prompt program: %v: %s", err, out)
	}
	if strings.Contains(string(out), "[Function") {
		t.Fatalf("prompt assignment echoed: %q", out)
	}
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, "_log") {
			t.Fatal("mongosh wrote a log in the private HOME", p)
		}
		return nil
	})
}
