// Package updater checks canonical releases and verifies explicit updates without
// touching AWS configuration, credentials, sessions, or application state.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/releases"
)

const apiRoot = "https://api.github.com/repos/ThamalDilanka/easy-sso"
const maxArchive = 128 << 20

var ErrNoRelease = errors.New("no compatible published release found")

type asset struct {
	Name string `json:"name"`
	ID   int64  `json:"id"`
}
type release struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}
type Candidate struct {
	Version       string
	archive, sums asset
	goos, arch    string
}
type Client struct {
	http  *http.Client
	token string
}

// New uses only GitHub's canonical repository and asset endpoints. A token is
// optional and sent only to api.github.com, never to redirected storage hosts.
func New(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		req.Header.Del("Authorization")
		if !allowedURL(req.URL) {
			return errors.New("untrusted release redirect")
		}
		return nil
	}}}
}
func allowedURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch u.Host {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func (c *Client) get(ctx context.Context, endpoint string, limit int64, binary bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiRoot+endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid release request")
	}
	req.Header.Set("User-Agent", "easy-sso-updater")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Accept", "application/vnd.github+json")
	if binary {
		req.Header.Set("Accept", "application/octet-stream")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("release request failed; check network connectivity, TLS, or timeout (credentials and URLs omitted)")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
	case 404:
		return nil, errors.New("release repository or asset unavailable; for private access set EASY_SSO_GITHUB_TOKEN with repository contents:read")
	case 401:
		return nil, errors.New("GitHub authentication failed; check EASY_SSO_GITHUB_TOKEN")
	case 403, 429:
		return nil, errors.New("GitHub denied or rate-limited the request; check token permissions or retry later")
	default:
		return nil, fmt.Errorf("GitHub release request returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, errors.New("release response exceeds size limit")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errors.New("release download interrupted")
	}
	if int64(len(b)) > limit {
		return nil, errors.New("release response exceeds size limit")
	}
	return b, nil
}

// Latest compares SemVer rather than release creation dates, skipping drafts,
// prereleases unless requested, and releases without the exact platform assets.
// Pagination is bounded; exceeding the bound fails rather than selecting from
// an incomplete list.
func (c *Client) Latest(ctx context.Context, goos, arch string, prerelease bool) (Candidate, error) {
	if _, err := releases.Asset("0.0.0", goos, arch); err != nil {
		return Candidate{}, err
	}
	var best Candidate
	var bestVersion releases.Version
	for page := 1; page <= 10; page++ {
		data, err := c.get(ctx, "/releases?per_page=100&page="+strconv.Itoa(page), 4<<20, false)
		if err != nil {
			return Candidate{}, err
		}
		var list []release
		if err = json.Unmarshal(data, &list); err != nil {
			return Candidate{}, errors.New("invalid GitHub release metadata")
		}
		for _, r := range list {
			v, err := releases.Parse(r.Tag)
			if err != nil || r.Tag != "v"+v.Text || r.Draft || v.Development() || (!prerelease && (r.Prerelease || v.Prerelease())) {
				continue
			}
			name, _ := releases.Asset(v.Text, goos, arch)
			cand := Candidate{Version: v.Text, goos: goos, arch: arch}
			counts := map[string]int{}
			for _, a := range r.Assets {
				counts[a.Name]++
				if a.Name == name {
					cand.archive = a
				}
				if a.Name == "SHA256SUMS" {
					cand.sums = a
				}
			}
			if cand.archive.ID <= 0 || cand.sums.ID <= 0 || counts[name] != 1 || counts["SHA256SUMS"] != 1 {
				continue
			}
			if best.Version == "" || v.Compare(bestVersion) > 0 {
				best, bestVersion = cand, v
			}
		}
		if len(list) < 100 {
			if best.Version == "" {
				return Candidate{}, ErrNoRelease
			}
			return best, nil
		}
	}
	return Candidate{}, errors.New("release listing exceeded 1000 entries; use the release page manually")
}
func UpgradeAvailable(current, latest string) (bool, error) {
	v, err := releases.Parse(current)
	if err != nil || v.Development() {
		return false, errors.New("development or unknown build; install a tagged release explicitly")
	}
	w, err := releases.Parse(latest)
	if err != nil {
		return false, err
	}
	return w.Compare(v) > 0, nil
}
func (c *Client) Download(ctx context.Context, candidate Candidate) ([]byte, error) {
	if candidate.archive.ID <= 0 || candidate.sums.ID <= 0 {
		return nil, errors.New("invalid release candidate")
	}
	sums, err := c.get(ctx, "/releases/assets/"+strconv.FormatInt(candidate.sums.ID, 10), 16<<10, true)
	if err != nil {
		return nil, err
	}
	expected, err := checksum(sums, candidate)
	if err != nil {
		return nil, err
	}
	payload, err := c.get(ctx, "/releases/assets/"+strconv.FormatInt(candidate.archive.ID, 10), maxArchive, true)
	if err != nil {
		return nil, err
	}
	return verifyArchive(payload, expected, candidate)
}
func ReleaseURL(version string) string {
	return "https://github.com/" + releases.Contract.Repository + "/releases/tag/v" + url.PathEscape(strings.TrimPrefix(version, "v"))
}
