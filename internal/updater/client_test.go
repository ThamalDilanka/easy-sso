package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/releases"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func fakeClient(t *testing.T, fn transport) *Client {
	t.Helper()
	c := New("test-token")
	c.http.Transport = fn
	return c
}
func releaseFixture(v string) release {
	return release{Tag: "v" + v, Assets: []asset{{Name: "easy-sso-" + v + "-linux-amd64.tar.gz", ID: 1}, {Name: "SHA256SUMS", ID: 2}}}
}
func TestLatestSelection(t *testing.T) {
	list := []release{releaseFixture("1.9.0"), releaseFixture("1.10.0"), releaseFixture("2.0.0-rc.1"), releaseFixture("3.0.0"), releaseFixture("4.0.0"), releaseFixture("5.0.0-dev")}
	list[3].Draft = true
	list[4].Assets = nil
	c := fakeClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || !strings.HasPrefix(r.URL.Path, "/repos/ThamalDilanka/easy-sso/releases") || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal("wrong canonical request")
		}
		b, _ := json.Marshal(list)
		return response(200, string(b)), nil
	})
	for _, pre := range []bool{false, true} {
		got, e := c.Latest(context.Background(), "linux", "amd64", pre)
		want := "1.10.0"
		if pre {
			want = "2.0.0-rc.1"
		}
		if e != nil || got.Version != want {
			t.Fatalf("%+v %v", got, e)
		}
	}
}
func TestLatestPagination(t *testing.T) {
	calls := 0
	c := fakeClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		list := []release{releaseFixture("2.0.0")}
		if calls == 1 {
			list = make([]release, 100)
			for i := range list {
				list[i] = releaseFixture("1.0.0")
			}
		}
		b, _ := json.Marshal(list)
		return response(200, string(b)), nil
	})
	got, e := c.Latest(context.Background(), "linux", "amd64", false)
	if e != nil || got.Version != "2.0.0" || calls != 2 {
		t.Fatalf("%+v %v calls=%d", got, e, calls)
	}
}
func TestCheckErrorsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{{"none", 200, "[]", "no compatible"}, {"private", 404, "secret", "contents:read"}, {"unauthorized", 401, "secret", "authentication"}, {"limit", 429, "secret", "rate-limited"}, {"forbidden", 403, "secret", "denied"}, {"badJSON", 200, "oops", "invalid"}, {"oversize", 200, strings.Repeat("x", (4<<20)+1), "size limit"}} {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(t, func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil })
			_, err := c.Latest(context.Background(), "linux", "amd64", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("%v", err)
			}
		})
	}
	c := fakeClient(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("offline with secret-token") })
	_, err := c.Latest(context.Background(), "linux", "amd64", false)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal(err)
	}
}
func TestRedirectPolicy(t *testing.T) {
	c := New("secret")
	for _, raw := range []string{"http://github.com/file", "https://evil.invalid/file", "https://api.github.com:444/file", "https://user@example.com/file"} {
		u, _ := url.Parse(raw)
		if allowedURL(u) {
			t.Fatal(raw)
		}
	}
	r, _ := http.NewRequest("GET", "https://release-assets.githubusercontent.com/file", nil)
	r.Header.Set("Authorization", "Bearer secret")
	if err := c.http.CheckRedirect(r, []*http.Request{r}); err != nil || r.Header.Get("Authorization") != "" {
		t.Fatal("redirect retained credentials")
	}
}
func TestUpgradePolicy(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		available, fail bool
	}{{"1.0.0", "1.0.1", true, false}, {"2.0.0", "1.0.0", false, false}, {"1.0.0+one", "1.0.0+two", false, false}, {"1.0.0-rc.1", "1.0.0", true, false}, {"0.1.0-dev", "1.0.0", false, true}, {"unknown", "1.0.0", false, true}} {
		a, e := UpgradeAvailable(tc.current, tc.latest)
		if a != tc.available || (e != nil) != tc.fail {
			t.Fatalf("%+v: %v %v", tc, a, e)
		}
	}
}

func TestDownloadFailureIsBounded(t *testing.T) {
	c := Candidate{Version: "1.2.3", goos: "linux", arch: "amd64", archive: asset{Name: "easy-sso-1.2.3-linux-amd64.tar.gz", ID: 1}, sums: asset{Name: "SHA256SUMS", ID: 2}}
	var sums strings.Builder
	for _, target := range releases.Contract.Targets {
		p := strings.Split(target, "-")
		name, _ := releases.Asset(c.Version, p[0], p[1])
		fmt.Fprintf(&sums, "%s  %s\n", strings.Repeat("a", 64), name)
	}
	for _, oversized := range []bool{false, true} {
		client := fakeClient(t, func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "/2") {
				return response(200, sums.String()), nil
			}
			result := response(200, "corrupt archive")
			if oversized {
				result.ContentLength = maxArchive + 1
			}
			return result, nil
		})
		_, err := client.Download(context.Background(), c)
		want := "checksum mismatch"
		if oversized {
			want = "size limit"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v", err)
		}
	}
}
