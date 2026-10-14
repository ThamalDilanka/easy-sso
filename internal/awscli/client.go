// Package awscli invokes the official CLI with isolated authentication contexts.
package awscli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/process"
	"github.com/ThamalDilanka/easy-sso/internal/store"
)

const OutputLimit = 1024 * 1024

type Client struct {
	path, config, credentials, emptyConfig, emptyCredentials string
	Timeout                                                  time.Duration
	LoginNotice                                              func(string)
}

func New(path, configPath, credentialsPath, stateDir string) (*Client, error) {
	if path == "" {
		return nil, errors.New("AWS CLI v2 is unavailable; install the official AWS CLI")
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return nil, errors.New("AWS CLI v2 executable unavailable")
	}
	if err = store.EnsureDir(stateDir); err != nil {
		return nil, err
	}
	c := &Client{path: resolved, config: configPath, credentials: credentialsPath, emptyConfig: filepath.Join(stateDir, "bound-config"), emptyCredentials: filepath.Join(stateDir, "bound-credentials"), Timeout: 45 * time.Second}
	for _, p := range []string{c.emptyConfig, c.emptyCredentials} {
		info, e := os.Lstat(p)
		if e == nil {
			if !info.Mode().IsRegular() {
				return nil, errors.New("unsafe bound CLI environment file")
			}
			if err := store.ValidatePrivate(p); err != nil {
				return nil, errors.New("unsafe bound CLI environment permissions")
			}
		}
		if e != nil && !os.IsNotExist(e) {
			return nil, e
		}
		if e = store.AtomicWrite(p, []byte{}); e != nil {
			return nil, e
		}
	}
	return c, nil
}
func (c *Client) HelperConfig() (string, string, string) { return c.path, c.config, c.credentials }

// DatabaseToken signs for the remote RDS endpoint, never the local SSM listener.
// run isolates this operation from ambient profiles and suppresses raw errors.
func (c *Client) DatabaseToken(ctx context.Context, s domain.Snapshot, db domain.Connection) (string, error) {
	if err := connections.Validate(db); err != nil {
		return "", err
	}
	if !db.IAMDatabase() {
		return "", errors.New("PostgreSQL IAM login configuration required")
	}
	out, err := c.run(ctx, &s, []string{"rds", "generate-db-auth-token", "--hostname", db.Endpoint, "--port", fmt.Sprint(db.DBPort), "--region", db.Region, "--username", db.DBUser}, false)
	if err != nil {
		return "", errors.New("IAM token generation failed; renew the selected role and check AWS CLI configuration")
	}
	token := strings.TrimSpace(string(out))
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("invalid IAM token response")
	}
	return token, nil
}
func ValidateSnapshot(s domain.Snapshot) error {
	if s.AccessKeyID == "" || s.SecretAccessKey == "" || s.SessionToken == "" {
		return errors.New("Activate this role before connecting: complete temporary credentials required")
	}
	for _, value := range []string{s.AccessKeyID, s.SecretAccessKey, s.SessionToken} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid temporary credential material")
		}
	}
	if !s.Expiration.After(time.Now().Add(30 * time.Second)) {
		return errors.New("Role credentials expired. Activate again to start new connections")
	}
	return nil
}
func (c *Client) environment(snapshot *domain.Snapshot) []string {
	allowed := map[string]bool{}
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "SystemRoot", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TERM", "COLORTERM", "DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "AWS_CA_BUNDLE"} {
		allowed[name] = true
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[key] {
			env = append(env, entry)
		}
	}
	config, credentials := c.config, c.emptyCredentials
	if snapshot != nil {
		config = c.emptyConfig
		credentials = c.emptyCredentials
	}
	env = append(env, "AWS_CONFIG_FILE="+config, "AWS_SHARED_CREDENTIALS_FILE="+credentials, "AWS_EC2_METADATA_DISABLED=true", "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off")
	if snapshot != nil {
		env = append(env, "AWS_ACCESS_KEY_ID="+snapshot.AccessKeyID, "AWS_SECRET_ACCESS_KEY="+snapshot.SecretAccessKey, "AWS_SESSION_TOKEN="+snapshot.SessionToken)
	}
	return env
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Len() int      { return b.buffer.Len() }
func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := OutputLimit - b.Len()
	if n > remaining {
		b.exceeded = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

type noticeWriter struct {
	callback func(string)
	used     int
	mu       sync.Mutex
}

func (w *noticeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if w.used >= OutputLimit {
		return n, nil
	}
	if len(p) > OutputLimit-w.used {
		p = p[:OutputLimit-w.used]
	}
	w.used += len(p)
	text := regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`).ReplaceAllString(string(p), "")
	text = strings.Map(func(r rune) rune {
		if r < ' ' && r != '\n' && r != '\t' {
			return -1
		}
		if r == 127 {
			return -1
		}
		return r
	}, text)
	w.callback(text)
	return n, nil
}
func (c *Client) run(ctx context.Context, snapshot *domain.Snapshot, args []string, login bool) ([]byte, error) {
	if snapshot != nil {
		if err := ValidateSnapshot(*snapshot); err != nil {
			return nil, err
		}
	}
	timeout := c.Timeout
	if login {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args = append(append([]string{}, args...), "--no-cli-pager", "--no-cli-auto-prompt")
	cmd := exec.CommandContext(ctx, c.path, args...)
	cmd.Env = c.environment(snapshot)
	var out, stderr limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if login && c.LoginNotice != nil {
		notice := &noticeWriter{callback: c.LoginNotice}
		cmd.Stdout = io.MultiWriter(&out, notice)
		cmd.Stderr = io.MultiWriter(&stderr, notice)
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("AWS CLI request interrupted: %w", ctx.Err())
	}
	if out.exceeded || stderr.exceeded {
		return nil, errors.New("AWS CLI response exceeded safe output limit")
	}
	// Errors deliberately omit arbitrary subprocess output: export and plugin
	// responses may contain access keys, tokens, stream URLs, or authorization codes.
	if err != nil {
		return nil, errors.New("AWS CLI request failed; check permissions, network, CLI configuration and SSO sign-in")
	}
	return out.Bytes(), nil
}
func (c *Client) Login(ctx context.Context, session string) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_.@+=,-]+$`).MatchString(session) {
		return errors.New("invalid SSO session name")
	}
	args := []string{"sso", "login", "--sso-session", session, "--use-device-code", "--no-browser"}
	_, err := c.run(ctx, nil, args, true)
	return err
}
func (c *Client) Logout(ctx context.Context) error {
	_, err := c.run(ctx, nil, []string{"sso", "logout"}, false)
	return err
}
func (c *Client) Export(ctx context.Context, p domain.Profile) (domain.Snapshot, error) {
	var s domain.Snapshot
	if p.Error != "" || p.Name == "" {
		return s, errors.New("unsupported source profile")
	}
	data, err := c.run(ctx, nil, []string{"configure", "export-credentials", "--profile", p.Name, "--format", "process"}, false)
	if err != nil {
		return s, err
	}
	var response struct {
		Version         int
		AccessKeyId     string
		SecretAccessKey string
		SessionToken    string
		Expiration      time.Time
	}
	if json.Unmarshal(data, &response) != nil || response.Version != 1 {
		return s, errors.New("invalid credential export response")
	}
	s = domain.Snapshot{AccessKeyID: response.AccessKeyId, SecretAccessKey: response.SecretAccessKey, SessionToken: response.SessionToken, Expiration: response.Expiration}
	if err = ValidateSnapshot(s); err != nil {
		return domain.Snapshot{}, err
	}
	return s, nil
}
func (c *Client) Identity(ctx context.Context, s domain.Snapshot, region string) (domain.Identity, error) {
	var id domain.Identity
	if !validRegion(region) {
		return id, errors.New("explicit AWS region required")
	}
	data, err := c.run(ctx, &s, []string{"sts", "get-caller-identity", "--region", region, "--output", "json"}, false)
	if err != nil {
		return id, err
	}
	if json.Unmarshal(data, &id) != nil || id.Account == "" || id.ARN == "" {
		return domain.Identity{}, errors.New("invalid STS identity response")
	}
	return id, nil
}
func validRegion(s string) bool {
	return regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`).MatchString(s)
}
func (c *Client) Start(ctx context.Context, s domain.Snapshot, args []string, in io.Reader, out, stderr io.Writer) (domain.Child, error) {
	if err := ValidateSnapshot(s); err != nil {
		return nil, err
	}
	if len(args) < 2 || args[0] != "ssm" || args[1] != "start-session" {
		return nil, errors.New("only official SSM start-session may be launched")
	}
	region := ""
	allowed := map[string]bool{"--region": true, "--target": true, "--reason": true, "--document-name": true, "--parameters": true}
	seen := map[string]bool{}
	target := ""
	for i := 2; i < len(args); i += 2 {
		option := args[i]
		if !allowed[option] || seen[option] || i+1 >= len(args) {
			return nil, errors.New("unsafe SSM option")
		}
		seen[option] = true
		value := args[i+1]
		if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid SSM argument")
		}
		if option == "--region" {
			region = value
		}
		if option == "--target" {
			target = value
		}
		if option == "--document-name" && value != "AWS-StartPortForwardingSessionToRemoteHost" {
			return nil, errors.New("unsupported SSM document")
		}
	}
	if !regexp.MustCompile(`^i-([0-9a-f]{8}|[0-9a-f]{17})$`).MatchString(target) {
		return nil, errors.New("invalid EC2 SSM target")
	}
	if !validRegion(region) {
		return nil, errors.New("explicit AWS region required")
	}
	args = append(append([]string{}, args...), "--no-cli-pager", "--no-cli-auto-prompt")
	cmd := exec.CommandContext(ctx, c.path, args...)
	cmd.Env = c.environment(&s)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = stderr
	return process.Start(cmd)
}
func (c *Client) Describe(ctx context.Context, s domain.Snapshot, region string, filters []string) ([]domain.SessionMetadata, error) {
	return c.describeSessions(ctx, s, region, filters, "Active")
}

func (c *Client) DescribeHistory(ctx context.Context, s domain.Snapshot, region string, filters []string) ([]domain.SessionMetadata, error) {
	return c.describeSessions(ctx, s, region, filters, "History")
}

func (c *Client) describeSessions(ctx context.Context, s domain.Snapshot, region string, filters []string, state string) ([]domain.SessionMetadata, error) {
	if !validRegion(region) {
		return nil, errors.New("explicit AWS region required")
	}
	args := []string{"ssm", "describe-sessions", "--state", state, "--region", region, "--output", "json"}
	for _, filter := range filters {
		if strings.HasPrefix(filter, "-") || strings.ContainsAny(filter, "\x00\r\n") {
			return nil, errors.New("invalid SSM metadata filter")
		}
	}
	if len(filters) > 0 {
		args = append(args, "--filters")
		args = append(args, filters...)
	}
	data, err := c.run(ctx, &s, args, false)
	if err != nil {
		return nil, err
	}
	var response struct {
		NextToken string
		Sessions  []struct {
			SessionID                     string `json:"SessionId"`
			Target, Owner, Reason, Status string
			StartDate                     json.RawMessage
		}
	}
	if json.Unmarshal(data, &response) != nil {
		return nil, errors.New("invalid SSM session metadata")
	}
	if response.NextToken != "" {
		return nil, errors.New("incomplete SSM session metadata; pagination was not completed")
	}
	result := make([]domain.SessionMetadata, 0, len(response.Sessions))
	for _, item := range response.Sessions {
		date := time.Time{}
		var raw string
		if json.Unmarshal(item.StartDate, &raw) == nil {
			date, _ = time.Parse(time.RFC3339Nano, raw)
		} else {
			var n float64
			if json.Unmarshal(item.StartDate, &n) == nil {
				date = time.Unix(int64(n), int64((n-float64(int64(n)))*1e9))
			}
		}
		result = append(result, domain.SessionMetadata{SessionID: item.SessionID, Target: item.Target, Owner: item.Owner, Reason: item.Reason, Status: item.Status, StartDate: date})
	}
	return result, nil
}
func (c *Client) Terminate(ctx context.Context, s domain.Snapshot, region, id string) error {
	if !validRegion(region) || !regexp.MustCompile(`^[A-Za-z0-9_.@+=,-]+$`).MatchString(id) {
		return errors.New("invalid exact SSM session identity")
	}
	_, err := c.run(ctx, &s, []string{"ssm", "terminate-session", "--session-id", id, "--region", region, "--output", "json"}, false)
	return err
}
