package database

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

// caServer serves a valid PEM bundle (its own certificate) and counts hits.
// It points caURLs["aws"] at itself and restores the package seams after.
func caServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, pem []byte)) (*atomic.Int32, []byte) {
	t.Helper()
	var hits atomic.Int32
	var body []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	body = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	urls, client := caURLs, caClient
	t.Cleanup(func() { caURLs, caClient = urls, client })
	if client.CheckRedirect == nil {
		t.Fatal("production CA client follows redirects")
	}
	caURLs = map[string]string{"aws": srv.URL + "/global-bundle.pem"}
	// Keep the production redirect policy; only trust the test server.
	caClient = &http.Client{Transport: srv.Client().Transport, CheckRedirect: client.CheckRedirect}
	return &hits, body
}

func serve(w http.ResponseWriter, _ *http.Request, pem []byte) { _, _ = w.Write(pem) }

func stateDir(t *testing.T) string {
	t.Helper()
	// A private directory, as the real state dir is (store refuses others).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err == nil {
		dir = filepath.Join(dir, "state")
		err = os.Mkdir(dir, 0o700)
	}
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCAPath(t *testing.T) {
	dir := stateDir(t)
	wd, _ := os.Getwd()
	for _, tc := range []struct {
		c    domain.Connection
		want string
	}{
		{domain.Connection{Partition: "aws", CAFile: "certs/ca.pem"}, filepath.Join(wd, "certs", "ca.pem")},
		{domain.Connection{Partition: "aws"}, filepath.Join(dir, "rds-ca-aws.pem")},
		{domain.Connection{Partition: "aws-us-gov"}, filepath.Join(dir, "rds-ca-aws-us-gov.pem")},
		{domain.Connection{Partition: "aws-cn", CAFile: filepath.Join(dir, "cn.pem")}, filepath.Join(dir, "cn.pem")},
	} {
		got, err := CAPath(tc.c, dir)
		if err != nil || got != tc.want {
			t.Errorf("CAPath(%+v) = %q, %v; want %q", tc.c, got, err, tc.want)
		}
	}
	if _, err := CAPath(domain.Connection{Partition: "aws-cn"}, dir); err == nil || !strings.Contains(err.Error(), "set the RDS CA file") {
		t.Fatalf("aws-cn without a file: %v", err)
	}
}

func TestEnsureCAUsesFreshCacheWithoutNetwork(t *testing.T) {
	hits, pem := caServer(t, serve)
	dir := stateDir(t)
	cached := filepath.Join(dir, "rds-ca-aws.pem")
	if err := os.WriteFile(cached, pem, 0o600); err != nil {
		t.Fatal(err)
	}
	caURLs["aws"] = "https://127.0.0.1:1/unreachable"
	got, err := EnsureCA(context.Background(), domain.Connection{Partition: "aws"}, dir)
	if err != nil || got != cached || hits.Load() != 0 {
		t.Fatalf("EnsureCA = %q, %v, hits %d", got, err, hits.Load())
	}
}

func TestEnsureCADownloadsWhenMissingOrStale(t *testing.T) {
	hits, pem := caServer(t, serve)
	dir := stateDir(t)
	c := domain.Connection{Partition: "aws"}
	want := filepath.Join(dir, "rds-ca-aws.pem")
	check := func(step string, wantHits int32) {
		t.Helper()
		got, err := EnsureCA(context.Background(), c, dir)
		if err != nil || got != want || hits.Load() != wantHits {
			t.Fatalf("%s: EnsureCA = %q, %v, hits %d", step, got, err, hits.Load())
		}
		data, _ := os.ReadFile(want)
		if string(data) != string(pem) {
			t.Fatalf("%s: cached bundle differs from download", step)
		}
		if info, _ := os.Stat(want); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: cache mode %v", step, info.Mode().Perm())
		}
	}
	check("missing", 1)
	if err := os.WriteFile(want, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-25 * time.Hour)
	_ = os.Chtimes(want, old, old)
	check("stale and invalid", 2)
	// The 24h rule alone: a valid file is refreshed only once it is stale.
	for _, tc := range []struct {
		age  time.Duration
		hits int32
	}{{time.Hour, 2}, {25 * time.Hour, 3}} {
		at := time.Now().Add(-tc.age)
		_ = os.Chtimes(want, at, at)
		check("valid aged "+tc.age.String(), tc.hits)
	}
}

func TestEnsureCARejectsBadDownloads(t *testing.T) {
	big := strings.Repeat("A", 1024*1024+1)
	for name, tc := range map[string]struct {
		handler func(http.ResponseWriter, *http.Request, []byte)
		want    string
	}{
		"status 500": {func(w http.ResponseWriter, _ *http.Request, pem []byte) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(pem)
		}, "AWS RDS CA bundle download was invalid"},
		"not PEM": {func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte("<html>")) }, "AWS RDS CA bundle download was invalid"},
		"over 1 MiB": {func(w http.ResponseWriter, _ *http.Request, pem []byte) {
			_, _ = w.Write(append(append([]byte{}, pem...), big...))
		}, "AWS RDS CA bundle download was invalid"},
		"redirect": {func(w http.ResponseWriter, r *http.Request, pem []byte) {
			if r.URL.Path == "/moved.pem" {
				_, _ = w.Write(pem)
				return
			}
			http.Redirect(w, r, "/moved.pem", http.StatusFound)
		}, "could not download the AWS RDS CA bundle; check network access or set a local CA file"},
	} {
		t.Run(name, func(t *testing.T) {
			caServer(t, tc.handler)
			dir := stateDir(t)
			_, err := EnsureCA(context.Background(), domain.Connection{Partition: "aws"}, dir)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("EnsureCA error = %v, want %q", err, tc.want)
			}
			if _, e := os.Stat(filepath.Join(dir, "rds-ca-aws.pem")); !os.IsNotExist(e) {
				t.Fatal("rejected download was cached")
			}
		})
	}
}

func TestEnsureCAHonoursCAFile(t *testing.T) {
	_, pem := caServer(t, func(http.ResponseWriter, *http.Request, []byte) { t.Error("CA file set but bundle downloaded") })
	dir := stateDir(t)
	file := filepath.Join(dir, "custom.pem")
	if err := os.WriteFile(file, pem, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureCA(context.Background(), domain.Connection{Partition: "aws", CAFile: file}, dir)
	if err != nil || got != file {
		t.Fatalf("EnsureCA = %q, %v", got, err)
	}
	_, err = EnsureCA(context.Background(), domain.Connection{Partition: "aws", CAFile: filepath.Join(dir, "missing.pem")}, dir)
	if err == nil || err.Error() != "CA file unavailable or invalid; select a PEM certificate bundle" {
		t.Fatalf("invalid CA file: %v", err)
	}
}

func TestPrepareChecksPsqlBeforeCA(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, _, err := Prepare(context.Background(), domain.Connection{Partition: "aws-cn"}, stateDir(t))
	if err == nil || !strings.HasPrefix(err.Error(), "PostgreSQL client missing") {
		t.Fatalf("Prepare = %v", err)
	}
}
