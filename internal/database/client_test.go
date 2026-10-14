package database

import (
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"os"
	"strings"
	"testing"
)

func TestClientRoutingIsolationAndStreamingRedaction(t *testing.T) {
	for _, key := range []string{"PGHOST", "PGSERVICE", "PGOPTIONS", "PGPASSWORD", "PSQLRC", "AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN"} {
		t.Setenv(key, "ambient-secret")
	}
	c := domain.Connection{Endpoint: "db.example.rds.amazonaws.com", LocalPort: 15432, DBPort: 5432, Database: "exampledb", DBUser: "iam_reader"}
	token := "db.example.rds.amazonaws.com:5432/?Action=connect&secret=TEST"
	cmd, output := Command("/usr/bin/psql", "/ca.pem", "test-run", token, c)
	args := strings.Join(cmd.Args, " ")
	if strings.Contains(args, token) {
		t.Fatal("token in argv")
	}
	env := strings.Join(cmd.Env, "\n")
	for _, required := range []string{"PGHOST=" + c.Endpoint, "PGHOSTADDR=127.0.0.1", "PGPORT=15432", "PGSSLMODE=verify-full", "PGSSLROOTCERT=/ca.pem", "PGPASSWORD=" + token, "PSQL_HISTORY=" + os.DevNull} {
		if !strings.Contains(env, required) {
			t.Fatal("missing client setting", required)
		}
	}
	if strings.Contains(env, "ambient-secret") || strings.Contains(env, "AWS_") {
		t.Fatal("ambient credentials inherited")
	}
	var shown strings.Builder
	text := "failure " + token + "\n" + output.marker + "exampledb=> "
	for _, b := range []byte(text) {
		shown.Write(output.Filter([]byte{b}))
	}
	if strings.Contains(shown.String(), token) || strings.Contains(shown.String(), "easy-sso-db-ready") || !strings.Contains(shown.String(), "[redacted]") {
		t.Fatal("unsafe output filtering")
	}
	select {
	case <-output.Ready:
	default:
		t.Fatal("split prompt not detected")
	}
}

func TestLoginFailureDoesNotConfirmConnection(t *testing.T) {
	for _, tc := range []struct{ text, want string }{{"FATAL: PAM authentication failed for user", "rds-db:connect"}, {"SSL error: certificate verify failed", "TLS"}, {"database does not exist", "database name"}} {
		_, output := Command("psql", "/ca", "test", "secret", domain.Connection{})
		for _, b := range []byte(tc.text) {
			output.Filter([]byte{b})
		}
		select {
		case <-output.Ready:
			t.Fatal("error counted as authenticated")
		default:
		}
		if !strings.Contains(output.Failure(), tc.want) {
			t.Fatal(output.Failure())
		}
	}
}
