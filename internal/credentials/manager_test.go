package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeCLI struct {
	calls                                 []string
	logoutErr, errorExport, errorIdentity error
	loginHook                             func()
	logoutHook                            func()
	expiration                            time.Time
	identityRegions                       []string
}

func (f *fakeCLI) Login(_ context.Context, s string) error {
	f.calls = append(f.calls, "login:"+s)
	if f.loginHook != nil {
		f.loginHook()
	}
	return nil
}
func (f *fakeCLI) Export(_ context.Context, p domain.Profile) (domain.Snapshot, error) {
	f.calls = append(f.calls, "export:"+p.Name)
	expiry := f.expiration
	if expiry.IsZero() {
		expiry = time.Now().Add(time.Hour)
	}
	return domain.Snapshot{AccessKeyID: "ASIATEST" + p.Name, SecretAccessKey: "PRIVATESECRET", SessionToken: "PRIVATETOKEN", Expiration: expiry}, f.errorExport
}
func (f *fakeCLI) Logout(context.Context) error {
	f.calls = append(f.calls, "logout")
	if f.logoutHook != nil {
		f.logoutHook()
	}
	return f.logoutErr
}
func (f *fakeCLI) Identity(_ context.Context, s domain.Snapshot, region string) (domain.Identity, error) {
	f.calls = append(f.calls, "identity")
	f.identityRegions = append(f.identityRegions, region)
	return domain.Identity{Account: "111122223333", ARN: "arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_ReadOnlyAccess_0123456789abcdef/user"}, f.errorIdentity
}

const configText = "[sso-session root]\nsso_start_url=https://example.awsapps.com/start\nsso_region=us-east-1\n[profile one]\nsso_session=root\nsso_account_id=111122223333\nsso_role_name=ReadOnlyAccess\nregion=us-east-1\n[profile two]\nsso_session=root\nsso_account_id=111122223333\nsso_role_name=ReadOnlyAccess\nregion=us-east-1\n"

func setup(t *testing.T) (*Manager, *fakeCLI, []domain.Selection) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(config, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	cli := &fakeCLI{}
	m, err := New(cli, config, filepath.Join(dir, "credentials"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := profiles.Load(config)
	if err != nil {
		t.Fatal(err)
	}
	return m, cli, []domain.Selection{{Profile: p[0], Alias: "tui-one"}, {Profile: p[1], Alias: "tui-two"}}
}
func TestBatchCleanupBeforePublishAndOwnership(t *testing.T) {
	m, cli, selected := setup(t)
	cli.logoutHook = func() {
		if _, err := os.Stat(m.credentials); !os.IsNotExist(err) {
			t.Fatal("credentials published before cleanup")
		}
	}
	result, err := m.Activate(context.Background(), selected, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatal(result)
	}
	if strings.Join(cli.calls, ",") != "login:root,export:one,identity,export:two,identity,logout" {
		t.Fatal(cli.calls)
	}
	data, err := os.ReadFile(m.path("credential-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE") || strings.Contains(string(data), "ASIATEST") {
		t.Fatal("secret in metadata")
	}
	if _, err = os.Stat(m.path("publication-pending.json")); !os.IsNotExist(err) {
		t.Fatal("journal left")
	}
	roles, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if r.Status != "active" {
			t.Fatal(r)
		}
	}
	_, snap, err := m.Snapshot("tui-one")
	if err != nil || snap.SessionToken != "PRIVATETOKEN" {
		t.Fatal(err)
	}
	if err = m.Remove("tui-one"); err != nil {
		t.Fatal(err)
	}
	roles, err = m.List()
	if err != nil || len(roles) != 1 || roles[0].Alias != "tui-two" {
		t.Fatal(roles, err)
	}
	source, _ := os.ReadFile(m.config)
	if !strings.Contains(string(source), "[profile one]") {
		t.Fatal("source profile removed")
	}
}

func TestActivationPublishesOnlyConfiguredRegion(t *testing.T) {
	m, cli, _ := setup(t)
	text := strings.ReplaceAll(configText, "us-east-1", "ap-southeast-2")
	text = strings.ReplaceAll(text, "\nregion=ap-southeast-2", "")
	if err := os.WriteFile(m.config, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := profiles.Load(m.config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate(context.Background(), []domain.Selection{{Profile: p[0], Alias: "custom-published"}}, true); err != nil {
		t.Fatal(err)
	}
	if len(cli.identityRegions) != 1 || cli.identityRegions[0] != "ap-southeast-2" {
		t.Fatalf("identity request used invented region: %v", cli.identityRegions)
	}
	cfg, err := os.ReadFile(m.config)
	if err != nil || !strings.Contains(string(cfg), "[profile custom-published]\nregion = ap-southeast-2\n") {
		t.Fatalf("published region did not follow config: %s, %v", cfg, err)
	}
}
func TestCleanupFailureAndRetry(t *testing.T) {
	m, cli, selected := setup(t)
	cli.logoutErr = errors.New("PRIVATETOKEN")
	_, err := m.Activate(context.Background(), selected, true)
	if err == nil || strings.Contains(err.Error(), "PRIVATETOKEN") {
		t.Fatal(err)
	}
	if _, err = os.Stat(m.credentials); !os.IsNotExist(err) {
		t.Fatal("published on cleanup failure")
	}
	if roles, e := m.List(); e == nil || len(roles) != 0 {
		t.Fatal("startup cleanup failure hidden with no managed roles", roles, e)
	}
	cli.logoutErr = nil
	if err = m.RetryCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(m.credentials); !os.IsNotExist(err) {
		t.Fatal("cleanup retry published discarded batch")
	}
	if _, err = m.Activate(context.Background(), selected, true); err != nil {
		t.Fatal(err)
	}
}
func TestFailureStatesAlwaysCleanup(t *testing.T) {
	for _, name := range []string{"export", "identity", "expired", "cancel"} {
		t.Run(name, func(t *testing.T) {
			m, cli, selected := setup(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "export":
				cli.errorExport = errors.New("secret")
			case "identity":
				cli.errorIdentity = errors.New("mismatch")
			case "expired":
				cli.expiration = time.Now().Add(-time.Hour)
			case "cancel":
				cli.loginHook = cancel
			}
			_, err := m.Activate(ctx, selected, true)
			if err == nil {
				t.Fatal("failure accepted")
			}
			if len(cli.calls) == 0 || cli.calls[len(cli.calls)-1] != "logout" {
				t.Fatal("cleanup absent", cli.calls)
			}
			if _, err = os.Stat(m.credentials); !os.IsNotExist(err) {
				t.Fatal("failure published")
			}
		})
	}
}
func TestCollisionPreflightAndAck(t *testing.T) {
	for _, name := range []string{"credentials", "config", "default", "duplicate", "ack", "stale"} {
		t.Run(name, func(t *testing.T) {
			m, cli, selections := setup(t)
			ack := true
			switch name {
			case "credentials":
				os.WriteFile(m.credentials, []byte("[tui-one]\naws_access_key_id=unmanaged\n"), 0600)
			case "config":
				os.WriteFile(m.config, []byte(configText+"[profile tui-one]\nregion=us-east-1\n"), 0600)
			case "default":
				selections[0].Alias = "default"
			case "duplicate":
				selections[1].Alias = selections[0].Alias
			case "ack":
				ack = false
			case "stale":
				selections[0].Profile.Role = "Other"
			}
			if _, err := m.Activate(context.Background(), selections, ack); err == nil {
				t.Fatal("preflight accepted")
			}
			if len(cli.calls) > 0 {
				t.Fatal("AWS invoked before preflight", cli.calls)
			}
		})
	}
}
func TestConcurrentEditsAndManagedConflicts(t *testing.T) {
	m, cli, selections := setup(t)
	cli.loginHook = func() { os.WriteFile(m.config, []byte(configText+"# externally changed\n"), 0600) }
	if _, err := m.Activate(context.Background(), selections, true); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.credentials); !os.IsNotExist(err) {
		t.Fatal("concurrent edit publication")
	}
	cli.loginHook = nil
	if _, err := m.Activate(context.Background(), selections, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.credentials)
	os.WriteFile(m.credentials, append(data, []byte("# user edit\n")...), 0600)
	if err := m.Remove("tui-two"); err == nil {
		t.Fatal("external edit removal accepted")
	}
	roles, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if roles[1].Status != "unknown" {
		t.Fatal(roles)
	}
	if _, _, err = m.Snapshot("tui-two"); err == nil {
		t.Fatal("external edit snapshot accepted")
	}
}
func TestActivationLock(t *testing.T) {
	m, cli, selections := setup(t)
	unlock, err := lock(m.path("activation.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err = m.Activate(context.Background(), selections, true); err == nil {
		t.Fatal("parallel activation allowed")
	}
	if len(cli.calls) > 0 {
		t.Fatal(cli.calls)
	}
}

func TestCredentialStatusTransitions(t *testing.T) {
	m, _, selections := setup(t)
	if _, err := m.Activate(context.Background(), selections[:1], true); err != nil {
		t.Fatal(err)
	}
	state, err := m.readState()
	if err != nil {
		t.Fatal(err)
	}
	state.Roles[0].Expiration = time.Now().Add(-time.Minute)
	if err = m.writeState(state); err != nil {
		t.Fatal(err)
	}
	roles, err := m.List()
	if err != nil || roles[0].Status != "expired" {
		t.Fatal(roles, err)
	}
	if _, _, err = m.Snapshot(selections[0].Alias); err == nil {
		t.Fatal("expired snapshot accepted")
	}
	if err = os.Remove(m.credentials); err != nil {
		t.Fatal(err)
	}
	roles, err = m.List()
	if err != nil || roles[0].Status != "missing" {
		t.Fatal(roles, err)
	}
}
func TestPrivatePathsAndPermissions(t *testing.T) {
	m, _, _ := setup(t)
	os.WriteFile(m.credentials, []byte("[unmanaged]\nkey=x\n"), 0644)
	if _, err := m.List(); err == nil {
		t.Fatal("unsafe file accepted")
	}
	os.Remove(m.credentials)
	os.Symlink(m.config, m.credentials)
	if _, err := m.List(); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestSharedAWSDirectoryRefusedBeforeSignIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics")
	}
	m, cli, selections := setup(t)
	dir := filepath.Dir(m.credentials)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	_, err := m.Activate(context.Background(), selections, true)
	if err == nil {
		t.Fatal("shared AWS directory accepted")
	}
	if len(cli.calls) > 0 {
		t.Fatal("sign-in started before the AWS directory was checked", cli.calls)
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "chmod 700") {
		t.Fatal("error does not name the directory and its fix:", err)
	}
	if _, err = os.Stat(m.path("cleanup-pending.json")); !os.IsNotExist(err) {
		t.Fatal("cleanup marker left by a refused activation")
	}
}
func TestRecoveryPublicationAndPartialRemoval(t *testing.T) {
	m, _, selected := setup(t)
	batch, err := m.Activate(context.Background(), selected, true)
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := os.ReadFile(m.credentials)
	text, _, _ := section(creds, batch[0].Alias)
	journal := publication{Roles: batch, CredentialHashes: map[string]string{batch[0].Alias: store.Fingerprint([]byte(text))}}
	data, _ := json.Marshal(journal)
	os.WriteFile(m.path("publication-pending.json"), data, 0600)
	recovered, err := New(m.cli, m.config, m.credentials, m.state)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := recovered.List()
	if err != nil || roles[0].Status != "unknown" {
		t.Fatal(roles, err)
	}
	if err = recovered.RetryCleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = recovered.Snapshot(batch[0].Alias); err == nil {
		t.Fatal("recovered interrupted publication became usable after cleanup without renewal")
	}
	config, _ := os.ReadFile(m.config)
	cfg, _, _ := section(config, configName(batch[0].Alias))
	removal := removal{Alias: batch[0].Alias, Fingerprint: batch[0].Fingerprint, ConfigHash: store.Fingerprint([]byte(cfg)), CredentialHash: store.Fingerprint([]byte(text))}
	data, _ = json.Marshal(removal)
	os.WriteFile(m.path("removal-pending.json"), data, 0600)
	os.WriteFile(m.credentials, replace(creds, batch[0].Alias, ""), 0600)
	recovered, err = New(m.cli, m.config, m.credentials, m.state)
	if err != nil {
		t.Fatal(err)
	}
	roles, err = recovered.List()
	if err != nil || len(roles) != 1 {
		t.Fatal(roles, err)
	}
}

func TestUnpublishParksKeysAndRepublishRestoresThem(t *testing.T) {
	m, _, selections := setup(t)
	if _, err := m.Activate(context.Background(), selections[:1], true); err != nil {
		t.Fatal(err)
	}
	alias := selections[0].Alias
	before, err := os.ReadFile(m.credentials)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Unpublish(alias); err != nil {
		t.Fatal(err)
	}
	creds, _ := os.ReadFile(m.credentials)
	if strings.Contains(string(creds), "["+alias+"]") || strings.Contains(string(creds), "PRIVATESECRET") {
		t.Fatal("unpublished keys still in the file")
	}
	config, _ := os.ReadFile(m.config)
	if !strings.Contains(string(config), "[profile "+alias+"]") {
		t.Fatal("unpublish removed the config section")
	}
	roles, err := m.List()
	if err != nil || len(roles) != 1 || roles[0].Status != "unpublished" || !roles[0].Unpublished {
		t.Fatal(roles, err)
	}
	if _, _, err := m.Snapshot(alias); err == nil || !strings.Contains(err.Error(), "unpublished") {
		t.Fatalf("unpublished snapshot: %v", err)
	}
	if err := m.Unpublish(alias); err == nil {
		t.Fatal("double unpublish accepted")
	}
	entries, _ := os.ReadDir(m.state)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(m.state, e.Name()))
		if strings.Contains(string(b), "PRIVATESECRET") || strings.Contains(string(b), "PRIVATETOKEN") {
			t.Fatalf("parked secret written to %s", e.Name())
		}
	}
	if err := m.Republish(alias); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(m.credentials); string(after) != string(before) {
		t.Fatalf("republish did not restore the exact section:\n%s\n---\n%s", before, after)
	}
	roles, _ = m.List()
	if roles[0].Status != "active" || roles[0].Unpublished {
		t.Fatal("republished role not active", roles)
	}
	if _, snap, err := m.Snapshot(alias); err != nil || snap.SecretAccessKey != "PRIVATESECRET" {
		t.Fatal("republished snapshot unusable", err)
	}
	if err := m.Republish(alias); err == nil {
		t.Fatal("republish of a published role accepted")
	}
	if err := m.Unpublish(alias); err != nil {
		t.Fatal(err)
	}
	state, _ := m.readState()
	state.Roles[0].Expiration = time.Now().Add(-time.Minute)
	if err := m.writeState(state); err != nil {
		t.Fatal(err)
	}
	roles, _ = m.List()
	if roles[0].Status != "expired" {
		t.Fatal("unpublished and expired role not shown as expired", roles)
	}
	if err := m.Republish(alias); err == nil || !strings.Contains(err.Error(), "renew") {
		t.Fatalf("expired republish: %v", err)
	}
}

func TestUnpublishedRoleAfterRestartNeedsRenew(t *testing.T) {
	m, cli, selections := setup(t)
	alias := selections[0].Alias
	if _, err := m.Activate(context.Background(), selections[:1], true); err != nil {
		t.Fatal(err)
	}
	if err := m.Unpublish(alias); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cli, m.config, m.credentials, m.state)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := restarted.List()
	if err != nil || roles[0].Status != "unpublished" {
		t.Fatal(roles, err)
	}
	if err := restarted.Republish(alias); err == nil || !strings.Contains(err.Error(), "renew") {
		t.Fatalf("republish without parked keys: %v", err)
	}
	if creds, _ := os.ReadFile(restarted.credentials); strings.Contains(string(creds), "["+alias+"]") {
		t.Fatal("republish wrote a section without keys")
	}
	if _, err := restarted.Activate(context.Background(), selections[:1], true); err != nil {
		t.Fatal(err)
	}
	roles, _ = restarted.List()
	if roles[0].Status != "active" || roles[0].Unpublished {
		t.Fatal("renew left the role unpublished", roles)
	}
	if err := restarted.Unpublish(alias); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if config, _ := os.ReadFile(restarted.config); strings.Contains(string(config), "[profile "+alias+"]") {
		t.Fatal("remove left the config section")
	}
	if roles, _ = restarted.List(); len(roles) != 0 {
		t.Fatal("state kept the removed role", roles)
	}
}

func TestUnpublishRefusesExternalEditsAndUnknownAliases(t *testing.T) {
	m, _, selections := setup(t)
	alias := selections[0].Alias
	if err := m.Unpublish(alias); err == nil {
		t.Fatal("unmanaged alias unpublished")
	}
	if _, err := m.Activate(context.Background(), selections[:1], true); err != nil {
		t.Fatal(err)
	}
	creds, _ := os.ReadFile(m.credentials)
	edited := strings.Replace(string(creds), "aws_session_token", "aws_session_token_edited", 1)
	if err := os.WriteFile(m.credentials, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Unpublish(alias); err == nil || !strings.Contains(err.Error(), "edited") {
		t.Fatalf("external edit not detected: %v", err)
	}
	if after, _ := os.ReadFile(m.credentials); string(after) != edited {
		t.Fatal("refused unpublish changed the file")
	}
	if err := os.WriteFile(m.credentials, creds, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Unpublish(alias); err != nil {
		t.Fatal(err)
	}
	// A section re-added by hand while unpublished is not ours to trust.
	if err := os.WriteFile(m.credentials, creds, 0600); err != nil {
		t.Fatal(err)
	}
	roles, _ := m.List()
	if roles[0].Status != "unknown" {
		t.Fatal("re-added section not flagged", roles)
	}
	if err := m.Republish(alias); err == nil {
		t.Fatal("republish over an external section accepted")
	}
}
