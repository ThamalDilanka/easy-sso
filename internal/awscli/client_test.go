package awscli

import (
	"context"
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeClient(t *testing.T, body string) *Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("local fake executable uses POSIX shell; native Windows verification is a release gate")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-aws")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	c, err := New(path, filepath.Join(dir, "config"), filepath.Join(dir, "credentials"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func snapshot() domain.Snapshot {
	return domain.Snapshot{AccessKeyID: "ASIATESTKEY", SecretAccessKey: "TESTSECRET", SessionToken: "TESTTOKEN", Expiration: time.Now().Add(time.Hour)}
}
func TestEnvironmentIsolatesProviders(t *testing.T) {
	for _, name := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_ENDPOINT_URL", "AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		t.Setenv(name, "unintended")
	}
	t.Setenv("AWS_CA_BUNDLE", "/approved-ca")
	t.Setenv("HTTPS_PROXY", "http://approved-proxy")
	c := fakeClient(t, "exit 0\n")
	for _, bound := range []bool{false, true} {
		var s *domain.Snapshot
		if bound {
			x := snapshot()
			s = &x
		}
		env := strings.Join(c.environment(s), "\n")
		if strings.Contains(env, "unintended") {
			t.Fatal("ambient provider inherited", env)
		}
		if !strings.Contains(env, "AWS_CA_BUNDLE=/approved-ca") || !strings.Contains(env, "AWS_EC2_METADATA_DISABLED=true") {
			t.Fatal(env)
		}
		if bound {
			if !strings.Contains(env, "AWS_ACCESS_KEY_ID=ASIATESTKEY") || strings.Contains(env, "AWS_CONFIG_FILE="+c.config+"\n") {
				t.Fatal(env)
			}
		} else if strings.Contains(env, "AWS_ACCESS_KEY_ID=") {
			t.Fatal("activation keys set")
		}
	}
}
func TestExportAndStructuredIdentity(t *testing.T) {
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	c := fakeClient(t, fmt.Sprintf("if [ \"$1\" = configure ]; then\n printf '%%s' '{\"Version\":1,\"AccessKeyId\":\"ASIATESTKEY\",\"SecretAccessKey\":\"TESTSECRET\",\"SessionToken\":\"TESTTOKEN\",\"Expiration\":\"%s\"}'\nelse\n printf '%%s' '{\"Account\":\"111122223333\",\"Arn\":\"arn:aws:sts::111122223333:assumed-role/Test/user\",\"UserId\":\"role:user\"}'\nfi\n", expiry))
	s, err := c.Export(context.Background(), domain.Profile{Name: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Identity(context.Background(), s, "us-east-1")
	if err != nil || id.Account != "111122223333" {
		t.Fatal(id, err)
	}
	if _, err = c.Identity(context.Background(), s, ""); err == nil {
		t.Fatal("missing region accepted")
	}
}
func TestExportRejectsStaticMalformedAndSecretErrors(t *testing.T) {
	for _, body := range []string{"printf '%s' '{\"Version\":1,\"AccessKeyId\":\"SECRET\",\"SecretAccessKey\":\"SECRET\"}'", "printf '%s' 'SECRET'; exit 1", "printf '%s' 'not JSON SECRET'", "printf '%s' '{\"Version\":2}'"} {
		c := fakeClient(t, body+"\n")
		_, err := c.Export(context.Background(), domain.Profile{Name: "selected"})
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
	}
}
func TestBoundedOutputAndTimeout(t *testing.T) {
	c := fakeClient(t, "head -c 1048577 /dev/zero\n")
	if _, err := c.run(context.Background(), nil, []string{"configure"}, false); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatal(err)
	}
	c = fakeClient(t, "exec sleep 5\n")
	c.Timeout = 20 * time.Millisecond
	if _, err := c.run(context.Background(), nil, []string{"configure"}, false); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatal(err)
	}
}
func TestSSMMetadataDatesAndExactTermination(t *testing.T) {
	c := fakeClient(t, "printf '%s' '{\"Sessions\":[{\"SessionId\":\"exact-id\",\"Target\":\"i-0123456789abcdef0\",\"Owner\":\"owner\",\"Reason\":\"easy-sso:run\",\"Status\":\"Connected\",\"StartDate\":\"2026-10-09T00:00:00Z\"},{\"SessionId\":\"other-id\",\"StartDate\":1791504000}]}'\n")
	items, err := c.Describe(context.Background(), snapshot(), "us-east-1", []string{"key=Target,value=i-0123456789abcdef0"})
	if err != nil || len(items) != 2 || items[0].SessionID != "exact-id" || items[0].StartDate.IsZero() || items[1].StartDate.IsZero() {
		t.Fatal(items, err)
	}
	if err = c.Terminate(context.Background(), snapshot(), "us-east-1", "bad\n-id"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err = c.Terminate(context.Background(), snapshot(), "us-east-1", "exact-id"); err != nil {
		t.Fatal(err)
	}
}
func TestLongLivedStartDoesNotUseMetadataTimeout(t *testing.T) {
	c := fakeClient(t, "sleep 0.1\n")
	c.Timeout = time.Millisecond
	child, err := c.Start(context.Background(), snapshot(), []string{"ssm", "start-session", "--region", "us-east-1", "--target", "i-0123456789abcdef0"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatal("short timeout applied to transport", err)
	}
	for _, args := range [][]string{{"ssm", "start-session", "--region", "us-east-1", "--profile", "other"}, {"ssm", "start-session", "--region", "us-east-1", "--debug"}, {"sts", "get-caller-identity"}} {
		if _, err = c.Start(context.Background(), snapshot(), args, nil, nil, nil); err == nil {
			t.Fatal("unsafe args accepted")
		}
	}
}
func TestTemporarySnapshotValidation(t *testing.T) {
	valid := snapshot()
	for _, s := range []domain.Snapshot{{}, {AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey, SessionToken: valid.SessionToken, Expiration: time.Now().Add(-time.Second)}, {AccessKeyID: "key\n", SecretAccessKey: valid.SecretAccessKey, SessionToken: valid.SessionToken, Expiration: valid.Expiration}} {
		if ValidateSnapshot(s) == nil {
			t.Fatal("unsafe snapshot accepted")
		}
	}
}
func TestLoginInstructionsAreTransient(t *testing.T) {
	c := fakeClient(t, "printf 'Open https://example.test with code ABC123\\n'\n")
	var notice string
	c.LoginNotice = func(s string) { notice += s }
	if err := c.Login(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "ABC123") {
		t.Fatal("official login instructions hidden")
	}
}

func TestLinuxLoginAuthorizationFlow(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux display detection")
	}
	for _, tc := range []struct {
		name, display, wayland string
		device                 bool
	}{
		{"headless", "", "", true},
		{"x11", ":0", "", true},
		{"wayland", "", "wayland-0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DISPLAY", tc.display)
			t.Setenv("WAYLAND_DISPLAY", tc.wayland)
			c := fakeClient(t, "printf '%s\\n' \"$@\"\n")
			var args string
			c.LoginNotice = func(s string) { args += s }
			if err := c.Login(context.Background(), "test-session"); err != nil {
				t.Fatal(err)
			}
			for _, flag := range []string{"--use-device-code", "--no-browser"} {
				if strings.Contains(args, flag) != tc.device {
					t.Fatalf("%s: unexpected authorization arguments %q", flag, args)
				}
			}
			if !strings.Contains(args, "--sso-session\ntest-session\n") {
				t.Fatal("selected session lost")
			}
		})
	}
}

func TestSSMHistoryUsesHistoryStateAndRejectsPartialResults(t *testing.T) {
	c := fakeClient(t, `case " $* " in
 *" --state History "*) printf '%s' '{"Sessions": [{"SessionId":"ended","Status":"Terminated","StartDate":1791504000}]}' ;;
 *) exit 1 ;;
esac
`)
	items, err := c.DescribeHistory(context.Background(), snapshot(), "us-east-1", nil)
	if err != nil || len(items) != 1 || items[0].Status != "Terminated" {
		t.Fatal(items, err)
	}
	partial := fakeClient(t, `printf '%s' '{"Sessions":[],"NextToken":"more"}'`)
	if _, err := partial.Describe(context.Background(), snapshot(), "us-east-1", nil); err == nil {
		t.Fatal("partial active query could falsely resolve recovery")
	}
	if _, err := partial.DescribeHistory(context.Background(), snapshot(), "us-east-1", nil); err == nil {
		t.Fatal("partial history query accepted")
	}
}
