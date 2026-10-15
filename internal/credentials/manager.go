// Package credentials implements cleanup-before-publication selected-role activation.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThamalDilanka/easy-sso/internal/awscli"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	"github.com/ThamalDilanka/easy-sso/internal/store"
)

type Manager struct {
	cli                        domain.CredentialCLI
	config, credentials, state string
	mu                         sync.Mutex
	// parked holds the exact credentials section of each paused alias. It is
	// never written to disk, so pausing leaves no key material in any file.
	parked map[string]string
}
type metadata struct {
	Version int                  `json:"version"`
	Roles   []domain.ManagedRole `json:"roles"`
}
type publication struct {
	Roles            []domain.ManagedRole `json:"roles"`
	CredentialHashes map[string]string    `json:"credentialHashes"`
}
type removal struct{ Alias, Fingerprint, ConfigHash, CredentialHash string }

func New(cli domain.CredentialCLI, configPath, credentialsPath, stateDir string) (*Manager, error) {
	if cli == nil {
		return nil, errors.New("AWS CLI service required")
	}
	if configPath == credentialsPath {
		return nil, errors.New("config and credentials paths must differ")
	}
	if err := store.EnsureDir(stateDir); err != nil {
		return nil, err
	}
	m := &Manager{cli: cli, config: configPath, credentials: credentialsPath, state: stateDir}
	if err := m.recoverPublication(); err != nil {
		return nil, err
	}
	return m, nil
}
func (m *Manager) path(name string) string { return filepath.Join(m.state, name) }
func (m *Manager) readState() (metadata, error) {
	out := metadata{Version: 1, Roles: []domain.ManagedRole{}}
	data, err := store.Read(m.path("credential-state.json"), 4*1024*1024)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err = store.ValidatePrivate(m.path("credential-state.json")); err != nil {
		return out, errors.New("unsafe credential ownership metadata permissions")
	}
	if len(data) > 4*1024*1024 || json.Unmarshal(data, &out) != nil || out.Version != 1 {
		return out, errors.New("invalid credential ownership metadata")
	}
	return out, nil
}
func (m *Manager) writeState(s metadata) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return store.AtomicWrite(m.path("credential-state.json"), data)
}
func readFile(path string, secret bool) ([]byte, error) {
	if err := store.CheckPath(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("unsafe credentials path or permissions; require private regular file")
	}
	if secret {
		if err := store.ValidatePrivate(path); err != nil {
			return nil, errors.New("unsafe credentials permissions; require current-user-only file")
		}
	}
	if info.Size() > 8*1024*1024 {
		return nil, errors.New("AWS profile file too large")
	}
	return store.Read(path, 8*1024*1024)
}

// section returns exact source text, including comments, for collision checks.
func section(data []byte, name string) (string, bool, error) {
	lines := strings.SplitAfter(string(data), "\n")
	var out strings.Builder
	active := false
	found := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			n := strings.TrimSpace(trim[1 : len(trim)-1])
			active = n == name
			if active {
				if found {
					return "", false, errors.New("duplicate profile sections")
				}
				found = true
			}
		}
		if active {
			out.WriteString(line)
		}
	}
	return out.String(), found, nil
}
func replace(data []byte, name, value string) []byte {
	var out strings.Builder
	skip := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			skip = strings.TrimSpace(trim[1:len(trim)-1]) == name
		}
		if !skip {
			out.WriteString(line)
		}
	}
	if value != "" {
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteByte('\n')
		}
		out.WriteString(value)
	}
	return []byte(out.String())
}
func configName(alias string) string {
	if alias == "default" {
		return "default"
	}
	return "profile " + alias
}
func fingerprint(config, credentials []byte, alias string) (string, error) {
	c, _, err := section(config, configName(alias))
	if err != nil {
		return "", err
	}
	k, _, err := section(credentials, alias)
	if err != nil {
		return "", err
	}
	return store.Fingerprint([]byte(c + "\x00" + k)), nil
}
func validateAlias(alias string) error {
	if alias == "default" || !regexp.MustCompile(`^[A-Za-z0-9_.@+=,-]{1,128}$`).MatchString(alias) {
		return errors.New("choose a valid named alias; default may not be replaced")
	}
	return nil
}
func (m *Manager) List() ([]domain.ManagedRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.readState()
	if err != nil {
		return nil, err
	}
	config, err := readFile(m.config, false)
	if err != nil {
		return nil, err
	}
	creds, err := readFile(m.credentials, true)
	if err != nil {
		return nil, err
	}
	_, cleanup := os.Stat(m.path("cleanup-pending.json"))
	for i := range s.Roles {
		r := &s.Roles[i]
		f, e := fingerprint(config, creds, r.Alias)
		_, exists, _ := section(creds, r.Alias)
		switch {
		case r.Status == "unknown":
			r.Status = "unknown"
		case r.Paused:
			switch {
			case exists || e != nil || f != r.Fingerprint || cleanup == nil:
				r.Status = "unknown"
			case !r.Expiration.After(time.Now()):
				r.Status = "expired"
			default:
				r.Status = "paused"
			}
		case !exists:
			r.Status = "missing"
		case e != nil || f != r.Fingerprint:
			r.Status = "unknown"
		case !r.Expiration.After(time.Now()):
			r.Status = "expired"
		case cleanup == nil:
			r.Status = "unknown"
		default:
			r.Status = "active"
		}
	}
	sort.Slice(s.Roles, func(i, j int) bool { return s.Roles[i].Alias < s.Roles[j].Alias })
	if cleanup == nil && len(s.Roles) == 0 {
		return s.Roles, errors.New("SSO cleanup incomplete or unknown. Credentials were not published. Open Credentials and retry cleanup")
	}
	return s.Roles, nil
}
func (m *Manager) Snapshot(alias string) (domain.ManagedRole, domain.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, marker := range []string{"cleanup-pending.json", "publication-pending.json"} {
		if _, err := os.Stat(m.path(marker)); err == nil {
			return domain.ManagedRole{}, domain.Snapshot{}, errors.New("credential state unknown; complete cleanup and explicitly renew the role before connecting")
		} else if !os.IsNotExist(err) {
			return domain.ManagedRole{}, domain.Snapshot{}, err
		}
	}
	s, err := m.readState()
	if err != nil {
		return domain.ManagedRole{}, domain.Snapshot{}, err
	}
	config, err := readFile(m.config, false)
	if err != nil {
		return domain.ManagedRole{}, domain.Snapshot{}, err
	}
	creds, err := readFile(m.credentials, true)
	if err != nil {
		return domain.ManagedRole{}, domain.Snapshot{}, err
	}
	for _, r := range s.Roles {
		if r.Alias != alias {
			continue
		}
		if r.Status == "unknown" {
			return r, domain.Snapshot{}, errors.New("credential publication was interrupted; explicitly renew this role before connecting")
		}
		if r.Paused {
			return r, domain.Snapshot{}, errors.New("credentials are paused; resume them in Credentials before connecting")
		}
		if _, exists, e := section(creds, alias); e != nil {
			return r, domain.Snapshot{}, e
		} else if !exists {
			return r, domain.Snapshot{}, errors.New("managed credentials missing; activate this role before connecting")
		}
		f, e := fingerprint(config, creds, alias)
		if e != nil || f != r.Fingerprint {
			return r, domain.Snapshot{}, errors.New("managed profile was externally edited; review required")
		}
		sections, e := profiles.Parse(creds)
		if e != nil {
			return r, domain.Snapshot{}, e
		}
		for _, item := range sections {
			if item.Name == alias {
				if item.Error != "" {
					return r, domain.Snapshot{}, errors.New("ambiguous credential entry")
				}
				snap := domain.Snapshot{AccessKeyID: item.Values["aws_access_key_id"], SecretAccessKey: item.Values["aws_secret_access_key"], SessionToken: item.Values["aws_session_token"], Expiration: r.Expiration}
				e = awscli.ValidateSnapshot(snap)
				return r, snap, e
			}
		}
		return r, domain.Snapshot{}, errors.New("managed credentials missing")
	}
	return domain.ManagedRole{}, domain.Snapshot{}, errors.New("Activate this role before connecting")
}
func (m *Manager) Activate(ctx context.Context, selections []domain.Selection, ack bool) ([]domain.ManagedRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ack {
		return nil, errors.New("acknowledge global AWS CLI SSO cleanup before activation")
	}
	if len(selections) == 0 {
		return nil, errors.New("select at least one role")
	}
	unlock, err := lock(m.path("activation.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err = os.Stat(m.path("cleanup-pending.json")); err == nil {
		return nil, errors.New("SSO cleanup incomplete. Retry cleanup before activation")
	}
	s, err := m.readState()
	if err != nil {
		return nil, err
	}
	config, err := readFile(m.config, false)
	if err != nil {
		return nil, err
	}
	creds, err := readFile(m.credentials, true)
	if err != nil {
		return nil, err
	}
	// Resolve fresh definitions so stale UI/config changes cannot silently retarget.
	loaded, err := profiles.Load(m.config)
	if err != nil {
		return nil, err
	}
	sources := map[string]domain.Profile{}
	for _, p := range loaded {
		sources[p.Name] = p
	}
	owned := map[string]domain.ManagedRole{}
	for _, r := range s.Roles {
		owned[r.Alias] = r
	}
	seen := map[string]bool{}
	for i := range selections {
		sel := &selections[i]
		if sel.Alias == "" {
			sel.Alias = "easy-sso-managed-" + sel.Profile.Name
		}
		if err = validateAlias(sel.Alias); err != nil {
			return nil, err
		}
		if seen[sel.Alias] {
			return nil, errors.New("duplicate published alias")
		}
		seen[sel.Alias] = true
		current, ok := sources[sel.Profile.Name]
		a, _ := json.Marshal(current)
		b, _ := json.Marshal(sel.Profile)
		if !ok || current.Error != "" || string(a) != string(b) {
			return nil, errors.New("source profile changed or is unsupported; refresh profile selection")
		}
		if current.Region == "" {
			return nil, fmt.Errorf("profile %q has no configured region; set region or its SSO session sso_region in AWS config", current.Name)
		}
		_, hasConfig, e := section(config, configName(sel.Alias))
		if e != nil {
			return nil, e
		}
		_, hasCreds, e := section(creds, sel.Alias)
		if e != nil {
			return nil, e
		}
		r, managed := owned[sel.Alias]
		if (hasConfig || hasCreds) && !managed {
			return nil, fmt.Errorf("alias %s collides with unmanaged profile", sel.Alias)
		}
		if managed {
			f, e := fingerprint(config, creds, sel.Alias)
			if e != nil || f != r.Fingerprint {
				return nil, errors.New("managed profile was externally edited; review required")
			}
		}
	}
	marker := []byte(`{"status":"cleanup incomplete or unknown"}`)
	if err = store.AtomicWrite(m.path("cleanup-pending.json"), marker); err != nil {
		return nil, err
	}
	// Once authentication begins, cleanup runs independently of cancellation.
	var failure error
	logged := map[string]bool{}
	snaps := make([]domain.Snapshot, len(selections))
	batch := make([]domain.ManagedRole, 0, len(selections))
	for _, sel := range selections {
		if !logged[sel.Profile.SSOSession] {
			logged[sel.Profile.SSOSession] = true
			if failure = m.cli.Login(ctx, sel.Profile.SSOSession); failure != nil {
				break
			}
		}
	}
	if failure == nil {
		for i, sel := range selections {
			snap, e := m.cli.Export(ctx, sel.Profile)
			if e == nil {
				e = awscli.ValidateSnapshot(snap)
			}
			if e != nil {
				failure = e
				break
			}
			id, e := m.cli.Identity(ctx, snap, sel.Profile.Region)
			if e == nil {
				e = profiles.ValidateIdentity(sel.Profile, id)
			}
			if e != nil {
				failure = e
				break
			}
			snaps[i] = snap
			batch = append(batch, domain.ManagedRole{Alias: sel.Alias, Profile: sel.Profile, Identity: id, Expiration: snap.Expiration, Status: "active"})
		}
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if e := m.cli.Logout(cleanupCtx); e != nil {
		return nil, errors.New("SSO cleanup failed. New credentials were not published. Retry cleanup")
	}
	if err = os.Remove(m.path("cleanup-pending.json")); err != nil {
		return nil, err
	}
	if failure != nil {
		return nil, errors.New("role activation failed; AWS CLI SSO cleanup complete; no new credentials published")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	writer, err := lock(m.path("publication.lock"))
	if err != nil {
		return nil, err
	}
	defer writer()
	freshConfig, err := readFile(m.config, false)
	if err != nil {
		return nil, err
	}
	freshCreds, err := readFile(m.credentials, true)
	if err != nil {
		return nil, err
	}
	if store.Fingerprint(config) != store.Fingerprint(freshConfig) || store.Fingerprint(creds) != store.Fingerprint(freshCreds) {
		return nil, errors.New("AWS files changed during sign-in; no new credentials published; retry review")
	}
	pending := publication{CredentialHashes: map[string]string{}}
	for i, r := range batch {
		snap := snaps[i]
		credText := fmt.Sprintf("[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\n", r.Alias, snap.AccessKeyID, snap.SecretAccessKey, snap.SessionToken)
		cfgText := fmt.Sprintf("[profile %s]\nregion = %s\noutput = json\n", r.Alias, r.Profile.Region)
		freshConfig = replace(freshConfig, configName(r.Alias), cfgText)
		freshCreds = replace(freshCreds, r.Alias, credText)
		pending.CredentialHashes[r.Alias] = store.Fingerprint([]byte(credText))
	}
	for i := range batch {
		batch[i].Fingerprint, err = fingerprint(freshConfig, freshCreds, batch[i].Alias)
		if err != nil {
			return nil, err
		}
		owned[batch[i].Alias] = batch[i]
	}
	s.Roles = nil
	for _, r := range owned {
		s.Roles = append(s.Roles, r)
	}
	pending.Roles = s.Roles
	data, _ := json.Marshal(pending)
	if err = store.AtomicWrite(m.path("publication-pending.json"), data); err != nil {
		return nil, err
	}
	if err = store.EnsureDir(filepath.Dir(m.credentials)); err != nil {
		return nil, err
	}
	if err = store.AtomicWrite(m.credentials, freshCreds); err != nil {
		return nil, err
	}
	if err = store.AtomicWrite(m.config, freshConfig); err != nil {
		return nil, err
	}
	if err = m.writeState(s); err != nil {
		return nil, err
	}
	if err = os.Remove(m.path("publication-pending.json")); err != nil {
		return nil, err
	}
	for _, r := range batch {
		delete(m.parked, r.Alias)
	}
	return batch, nil
}
func (m *Manager) RetryCleanup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lock(m.path("activation.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err = m.cli.Logout(ctx); err != nil {
		return errors.New("SSO cleanup failed. Retry cleanup")
	}
	if err = os.Remove(m.path("cleanup-pending.json")); os.IsNotExist(err) {
		return nil
	}
	return err
}
func (m *Manager) Remove(alias string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lock(m.path("activation.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	writer, err := lock(m.path("publication.lock"))
	if err != nil {
		return err
	}
	defer writer()
	s, err := m.readState()
	if err != nil {
		return err
	}
	config, err := readFile(m.config, false)
	if err != nil {
		return err
	}
	creds, err := readFile(m.credentials, true)
	if err != nil {
		return err
	}
	found := false
	roles := []domain.ManagedRole{}
	for _, r := range s.Roles {
		if r.Alias != alias {
			roles = append(roles, r)
			continue
		}
		found = true
		f, e := fingerprint(config, creds, alias)
		if e != nil || f != r.Fingerprint {
			return errors.New("managed profile was externally edited; removal blocked")
		}
	}
	if !found {
		return errors.New("alias is not application-owned")
	}
	s.Roles = roles
	// Removal writes no credential backup; a metadata journal lets restart finish
	// deleting exactly this previously verified ownership fingerprint.
	cfgText, _, _ := section(config, configName(alias))
	credText, _, _ := section(creds, alias)
	journal := removal{Alias: alias, ConfigHash: store.Fingerprint([]byte(cfgText)), CredentialHash: store.Fingerprint([]byte(credText))}
	journal.Fingerprint, _ = fingerprint(config, creds, alias)
	data, _ := json.Marshal(journal)
	if err = store.AtomicWrite(m.path("removal-pending.json"), data); err != nil {
		return err
	}
	if err = store.AtomicWrite(m.credentials, replace(creds, alias, "")); err != nil {
		return err
	}
	if err = store.AtomicWrite(m.config, replace(config, configName(alias), "")); err != nil {
		return err
	}
	if err = m.writeState(s); err != nil {
		return err
	}
	delete(m.parked, alias)
	return os.Remove(m.path("removal-pending.json"))
}

// Pause removes the alias's credentials section from the shared file and keeps
// its exact text only in memory; the config section stays so the profile name
// remains known. Resume puts the same text back without a new sign-in.
func (m *Manager) Pause(alias string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.withPublication(func(s *metadata, config, creds []byte) error {
		for i := range s.Roles {
			r := &s.Roles[i]
			if r.Alias != alias {
				continue
			}
			if r.Paused {
				return errors.New("credentials are already paused")
			}
			if r.Status == "unknown" {
				return errors.New("credential publication was interrupted; renew this role instead")
			}
			f, e := fingerprint(config, creds, alias)
			if e != nil || f != r.Fingerprint {
				return errors.New("managed profile was externally edited; pause blocked")
			}
			text, exists, e := section(creds, alias)
			if e != nil {
				return e
			}
			if !exists {
				return errors.New("managed credentials missing; renew this role instead")
			}
			next := replace(creds, alias, "")
			if e = store.AtomicWrite(m.credentials, next); e != nil {
				return e
			}
			if m.parked == nil {
				m.parked = map[string]string{}
			}
			m.parked[alias] = text
			r.Paused = true
			if r.Fingerprint, e = fingerprint(config, next, alias); e != nil {
				return e
			}
			return m.writeState(*s)
		}
		return errors.New("alias is not application-owned")
	})
}

// Resume restores a paused alias's credentials section from memory. After a
// restart the parked text is gone and the role must be renewed instead.
func (m *Manager) Resume(alias string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.withPublication(func(s *metadata, config, creds []byte) error {
		for i := range s.Roles {
			r := &s.Roles[i]
			if r.Alias != alias {
				continue
			}
			if !r.Paused {
				return errors.New("credentials are not paused")
			}
			if !r.Expiration.After(time.Now()) {
				return errors.New("credentials expired while paused; renew the role")
			}
			text, ok := m.parked[alias]
			if !ok {
				return errors.New("paused keys were dropped when easy-sso exited; renew the role")
			}
			if _, exists, e := section(creds, alias); e != nil {
				return e
			} else if exists {
				return errors.New("a profile with this name was added externally while paused; review the credentials file")
			}
			f, e := fingerprint(config, creds, alias)
			if e != nil || f != r.Fingerprint {
				return errors.New("managed profile was externally edited; resume blocked")
			}
			next := replace(creds, alias, text)
			if e = store.AtomicWrite(m.credentials, next); e != nil {
				return e
			}
			delete(m.parked, alias)
			r.Paused = false
			if r.Fingerprint, e = fingerprint(config, next, alias); e != nil {
				return e
			}
			return m.writeState(*s)
		}
		return errors.New("alias is not application-owned")
	})
}

// withPublication runs fn with the ownership metadata and both AWS files read
// under the activation and publication locks, like Remove.
func (m *Manager) withPublication(fn func(s *metadata, config, creds []byte) error) error {
	unlock, err := lock(m.path("activation.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	writer, err := lock(m.path("publication.lock"))
	if err != nil {
		return err
	}
	defer writer()
	s, err := m.readState()
	if err != nil {
		return err
	}
	config, err := readFile(m.config, false)
	if err != nil {
		return err
	}
	creds, err := readFile(m.credentials, true)
	if err != nil {
		return err
	}
	return fn(&s, config, creds)
}
func (m *Manager) recoverPublication() error {
	pendingRecovery := false
	for _, name := range []string{"publication-pending.json", "removal-pending.json"} {
		if _, err := os.Stat(m.path(name)); err == nil {
			pendingRecovery = true
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if !pendingRecovery {
		return nil
	}
	unlock, err := lock(m.path("activation.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	data, err := os.ReadFile(m.path("publication-pending.json"))
	if err == nil {
		var pending publication
		if len(data) > 4*1024*1024 || json.Unmarshal(data, &pending) != nil {
			return errors.New("invalid publication recovery journal")
		}
		config, e := readFile(m.config, false)
		if e != nil {
			return e
		}
		creds, e := readFile(m.credentials, true)
		if e != nil {
			return e
		}
		s, e := m.readState()
		if e != nil {
			return e
		}
		owned := map[string]domain.ManagedRole{}
		for _, r := range s.Roles {
			owned[r.Alias] = r
		}
		for _, r := range pending.Roles {
			expected, selected := pending.CredentialHashes[r.Alias]
			if !selected {
				continue
			}
			text, exists, e := section(creds, r.Alias)
			if e != nil {
				return e
			}
			if exists && store.Fingerprint([]byte(text)) == expected {
				r.Fingerprint, e = fingerprint(config, creds, r.Alias)
				if e != nil {
					return e
				}
				r.Status = "unknown"
				owned[r.Alias] = r
			}
		}
		s.Roles = nil
		for _, r := range owned {
			s.Roles = append(s.Roles, r)
		}
		if e = m.writeState(s); e != nil {
			return e
		}
		if e = store.AtomicWrite(m.path("cleanup-pending.json"), []byte(`{"status":"publication interrupted; cleanup state unknown"}`)); e != nil {
			return e
		}
		if e = os.Remove(m.path("publication-pending.json")); e != nil {
			return e
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err = os.ReadFile(m.path("removal-pending.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending removal
	if json.Unmarshal(data, &pending) != nil || validateAlias(pending.Alias) != nil {
		return errors.New("invalid removal recovery journal")
	}
	// Never continue a deletion after external edits. Already completed partial
	// removals are safe only when remaining sections still match the recorded role.
	s, err := m.readState()
	if err != nil {
		return err
	}
	config, err := readFile(m.config, false)
	if err != nil {
		return err
	}
	creds, err := readFile(m.credentials, true)
	if err != nil {
		return err
	}
	cfgText, c, e := section(config, configName(pending.Alias))
	if e != nil {
		return e
	}
	credText, k, e := section(creds, pending.Alias)
	if e != nil {
		return e
	}
	if (c && store.Fingerprint([]byte(cfgText)) != pending.ConfigHash) || (k && store.Fingerprint([]byte(credText)) != pending.CredentialHash) {
		return errors.New("profile removal interrupted with external edits; review required")
	}
	if k {
		if err = store.AtomicWrite(m.credentials, replace(creds, pending.Alias, "")); err != nil {
			return err
		}
	}
	if c {
		if err = store.AtomicWrite(m.config, replace(config, configName(pending.Alias), "")); err != nil {
			return err
		}
	}
	roles := []domain.ManagedRole{}
	for _, r := range s.Roles {
		if r.Alias != pending.Alias {
			roles = append(roles, r)
		}
	}
	s.Roles = roles
	if err = m.writeState(s); err != nil {
		return err
	}
	return os.Remove(m.path("removal-pending.json"))
}
