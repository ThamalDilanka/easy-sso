// Package connections implements strict portable, credential-free bookmarks.
package connections

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/store"
	"github.com/ThamalDilanka/easy-sso/spec"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const MaxBytes = 5 * 1024 * 1024

var schema *jsonschema.Schema
var schemaErr error
var schemaOnce sync.Once

type document struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Connections   []domain.Connection `json:"connections"`
}
type Manager struct {
	path      string
	profiles  map[string]bool
	protected map[string]bool
	mu        sync.Mutex
}

func New(stateDir string) (*Manager, error) {
	if err := store.EnsureDir(stateDir); err != nil {
		return nil, err
	}
	m := &Manager{path: filepath.Join(stateDir, "connections.json"), profiles: map[string]bool{}, protected: map[string]bool{}}
	for _, name := range []string{"connections.json", "settings.json", "credential-state.json", "bound-config", "bound-credentials", "cleanup-pending.json", "publication-pending.json", "removal-pending.json"} {
		p, _ := filepath.Abs(filepath.Join(stateDir, name))
		m.protected[p] = true
	}
	_, err := m.List()
	return m, err
}
func (m *Manager) Path() string { return m.path }
func (m *Manager) SetProtectedPaths(paths ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range paths {
		a, e := filepath.Abs(p)
		if e == nil {
			m.protected[a] = true
		}
	}
}
func (m *Manager) SetProfiles(ps []domain.Profile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.profiles = map[string]bool{}
	for _, p := range ps {
		if p.Error == "" {
			m.profiles[p.Name] = true
		}
	}
}
func getSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		var raw any
		schemaErr = json.Unmarshal(spec.ConnectionsSchema, &raw)
		if schemaErr != nil {
			return
		}
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		c.AssertFormat()
		schemaErr = c.AddResource("https://easy-sso.local/connections.schema.json", raw)
		if schemaErr == nil {
			schema, schemaErr = c.Compile("https://easy-sso.local/connections.schema.json")
		}
	})
	return schema, schemaErr
}
func decode(b []byte) (document, error) {
	var d document
	if len(b) > MaxBytes {
		return d, errors.New("bookmark import exceeds 5 MiB limit")
	}
	if err := rejectDuplicateKeys(b); err != nil {
		return d, err
	}
	var raw any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return d, errors.New("invalid bookmark JSON")
	}
	sch, err := getSchema()
	if err != nil {
		return d, err
	}
	if err = sch.Validate(raw); err != nil {
		return d, errors.New("bookmark schema validation failed; check version, fields, formats and record limits")
	}
	if err = json.Unmarshal(b, &d); err != nil {
		return d, errors.New("invalid bookmark field types")
	}
	ids, keys := map[string]bool{}, map[string]bool{}
	for i := range d.Connections {
		c := &d.Connections[i]
		c.Endpoint = strings.ToLower(c.Endpoint)
		if err = Validate(*c); err != nil {
			return d, fmt.Errorf("bookmark %d: %w", i+1, err)
		}
		key := ResourceKey(*c)
		if ids[c.ID] || keys[key] {
			return d, errors.New("duplicate bookmark id or resource identity")
		}
		ids[c.ID], keys[key] = true, true
	}
	return d, nil
}
func Validate(c domain.Connection) error {
	for _, s := range []string{c.ID, c.Name, c.LastUsedProfile, c.LastUsedRole, c.Endpoint, c.Region, c.RelayRegion, c.Database, c.DBUser, c.CAFile, c.DocumentName} {
		if strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
			return errors.New("control characters are not allowed")
		}
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("display name is required")
	}
	if c.DBAccountID != "" && c.DBAccountID != c.AccountID {
		return errors.New("cross-account database ownership is deferred; declared DB account must match relay account")
	}
	if c.Kind == "db" {
		if c.Endpoint != strings.TrimSpace(c.Endpoint) || strings.ContainsAny(c.Endpoint, "/@:?%\\") {
			return errors.New("database endpoint must be a hostname without URL, credentials or port")
		}
		if c.Endpoint == "localhost" || !strings.Contains(c.Endpoint, ".") {
			return errors.New("database endpoint must be a remote hostname")
		}
	}
	if c.IAMDatabase() && (strings.TrimSpace(c.Database) == "" || strings.TrimSpace(c.DBUser) == "") {
		return errors.New("PostgreSQL IAM login requires a database name and database username")
	}
	if c.DocumentName != "" {
		if c.Kind != "ec2" {
			return errors.New("session documents apply only to EC2 bookmarks")
		}
		d, err := ParseSessionDocument(c.DocumentName)
		if err != nil {
			return err
		}
		// SSM documents are regional; a shared ARN must sit beside the instance.
		if d.ARN && (d.Partition != c.Partition || d.Region != c.Region) {
			return errors.New("session document ARN must be in the bookmark's partition and region")
		}
	}
	// Validate an individual record against the same strict schema, with a
	// placeholder only for the ID that is assigned on successful first save.
	if c.ID == "" {
		c.ID = "pending"
	}
	rawBytes, _ := json.Marshal(document{1, []domain.Connection{c}})
	var raw any
	_ = json.Unmarshal(rawBytes, &raw)
	sch, err := getSchema()
	if err != nil {
		return err
	}
	if err = sch.Validate(raw); err != nil {
		return errors.New("invalid connection fields: check account, instance, regions and ports")
	}
	return nil
}
func CompatibleRegion(partition, region string) bool {
	switch partition {
	case "aws-cn":
		return strings.HasPrefix(region, "cn-")
	case "aws-us-gov":
		return strings.HasPrefix(region, "us-gov-")
	case "aws":
		return !strings.HasPrefix(region, "cn-") && !strings.HasPrefix(region, "us-gov-")
	default:
		return false
	}
}
func ResourceKey(c domain.Connection) string {
	if c.Kind == "ec2" {
		parts := []string{c.Kind, c.Partition, c.AccountID, c.Region, c.InstanceID}
		// Only a custom document extends the key, so existing keys stay stable.
		if c.DocumentName != "" {
			parts = append(parts, c.DocumentName)
		}
		return strings.Join(parts, "|")
	}
	parts := []string{c.Kind, c.Partition, c.AccountID, strings.ToLower(c.Endpoint), fmt.Sprint(c.DBPort), c.Region, c.RelayInstanceID, c.RelayRegion}
	if c.IAMDatabase() {
		login, _ := json.Marshal([]string{c.AuthMode, c.Database, c.DBUser})
		parts = append(parts, string(login))
	}
	return strings.Join(parts, "|")
}
func (m *Manager) read() ([]domain.Connection, []byte, error) {
	b, err := store.Read(m.path, MaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Connection{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	d, err := decode(b)
	return d.Connections, b, err
}
func (m *Manager) List() ([]domain.Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cs, _, err := m.read()
	return cs, err
}
func (m *Manager) write(cs []domain.Connection) error {
	if cs == nil {
		cs = []domain.Connection{}
	}
	b, err := json.MarshalIndent(document{1, cs}, "", "  ")
	if err != nil {
		return err
	}
	if _, err = decode(b); err != nil {
		return err
	}
	return store.AtomicWrite(m.path, append(b, '\n'))
}
func (m *Manager) Save(c domain.Connection) error {
	if err := Validate(c); err != nil {
		return err
	}
	if err := DocumentDBMode(c); err != nil {
		return err
	}
	c.Endpoint = strings.ToLower(c.Endpoint)
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := store.Lock(m.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	cs, _, err := m.read()
	if err != nil {
		return err
	}
	for i, v := range cs {
		if ResourceKey(v) == ResourceKey(c) {
			c.ID = v.ID
			cs[i] = c
			return m.write(cs)
		}
		if c.ID != "" && c.ID == v.ID {
			return errors.New("bookmark ID already belongs to another resource")
		}
	}
	if c.ID == "" {
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return err
		}
		c.ID = hex.EncodeToString(id[:])
	}
	return m.write(append(cs, c))
}

// Update changes a saved bookmark without changing live session snapshots.
func (m *Manager) Update(c domain.Connection) error {
	if c.ID == "" {
		return errors.New("select a saved bookmark to edit")
	}
	if err := Validate(c); err != nil {
		return err
	}
	if err := DocumentDBMode(c); err != nil {
		return err
	}
	c.Endpoint = strings.ToLower(c.Endpoint)
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := store.Lock(m.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	cs, _, err := m.read()
	if err != nil {
		return err
	}
	index := -1
	for i, old := range cs {
		if old.ID == c.ID {
			if old.Kind != c.Kind || old.AccountID != c.AccountID || old.Partition != c.Partition {
				return errors.New("bookmark kind and account cannot be changed")
			}
			index = i
		} else if ResourceKey(old) == ResourceKey(c) {
			return errors.New("another bookmark already uses this destination")
		}
	}
	if index < 0 {
		return errors.New("bookmark no longer exists; reload and try again")
	}
	// Usage metadata belongs to the last connection, not this edit.
	c.LastUsedAt, c.LastUsedProfile, c.LastUsedRole = cs[index].LastUsedAt, cs[index].LastUsedProfile, cs[index].LastUsedRole
	cs[index] = c
	return m.write(cs)
}

func (m *Manager) Export(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	runtimeDir, _ := filepath.Abs(filepath.Join(filepath.Dir(m.path), "runtime"))
	relative, _ := filepath.Rel(runtimeDir, abs)
	if m.protected[abs] || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) || strings.HasSuffix(abs, ".lock") {
		return errors.New("export destination is reserved for AWS or application state; choose a new backup file")
	}
	cs, _, err := m.read()
	if err != nil {
		return err
	}
	if cs == nil {
		cs = []domain.Connection{}
	}
	b, err := json.MarshalIndent(document{1, cs}, "", "  ")
	if err != nil {
		return err
	}
	return store.ExportWrite(path, append(b, '\n'))
}
func (m *Manager) Preview(path string) (domain.ImportPlan, error) {
	var plan domain.ImportPlan
	b, err := store.Read(path, MaxBytes)
	if err != nil {
		return plan, err
	}
	d, err := decode(b)
	if err != nil {
		return plan, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	local, current, err := m.read()
	if err != nil {
		return plan, err
	}
	plan.Existing = local
	plan.Fingerprint = store.Fingerprint(current)
	for _, c := range d.Connections {
		conflict := false
		for _, v := range local {
			if c.ID == v.ID || ResourceKey(c) == ResourceKey(v) {
				conflict = true
				break
			}
		}
		if conflict {
			plan.Conflicts = append(plan.Conflicts, c)
		} else {
			plan.Additions = append(plan.Additions, c)
		}
		if c.LastUsedProfile != "" && !m.profiles[c.LastUsedProfile] {
			plan.Unbound = append(plan.Unbound, c.ID)
		}
	}
	return plan, nil
}
func (m *Manager) Commit(plan domain.ImportPlan, replace bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := store.Lock(m.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	cs, b, err := m.read()
	if err != nil {
		return err
	}
	if store.Fingerprint(b) != plan.Fingerprint {
		return errors.New("bookmarks changed after preview; import again")
	}
	// Never trust a caller-modified plan; full merged validation happens before write.
	incoming := plan.Additions
	if replace {
		incoming = append(append([]domain.Connection{}, incoming...), plan.Conflicts...)
	}
	// Only records this import writes are checked, so a legacy one on disk
	// never blocks an unrelated import.
	for _, c := range incoming {
		if err = DocumentDBMode(c); err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
	}
	cs = append(cs, plan.Additions...)
	if replace {
		for _, c := range plan.Conflicts {
			var kept []domain.Connection
			for _, v := range cs {
				if v.ID != c.ID && ResourceKey(v) != ResourceKey(c) {
					kept = append(kept, v)
				}
			}
			cs = append(kept, c)
		}
	}
	return m.write(cs)
}

// encoding/json accepts duplicate object keys. Reject them before schema
// validation so a preview never has an ambiguous field interpretation.
func rejectDuplicateKeys(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var value func() error
	value = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, e := dec.Token()
				if e != nil {
					return e
				}
				k := key.(string)
				if seen[k] {
					return errors.New("duplicate JSON property")
				}
				seen[k] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for dec.More() {
				if err = value(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON")
		}
		_, err = dec.Token()
		return err
	}
	if err := value(); err != nil {
		return errors.New("invalid or ambiguous bookmark JSON")
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("extra data after bookmark JSON")
	}
	return nil
}
