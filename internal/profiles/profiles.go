// Package profiles resolves configured SSO identities without executing providers.
package profiles

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

const MaxDepth = 32

type Section struct {
	Name   string
	Values map[string]string
	Error  string
}

// Parse deliberately rejects ambiguous sections and duplicate keys. It never
// evaluates credential_process, shell syntax, or interpolation.
func Parse(data []byte) ([]Section, error) {
	var sections []Section
	seen := map[string]int{}
	current := -1
	s := bufio.NewScanner(strings.NewReader(string(data)))
	s.Buffer(make([]byte, 4096), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" {
				return nil, errors.New("empty config section")
			}
			sections = append(sections, Section{Name: name, Values: map[string]string{}})
			current = len(sections) - 1
			if old, ok := seen[name]; ok {
				sections[old].Error = "duplicate section"
				sections[current].Error = "duplicate section"
			}
			seen[name] = current
			continue
		}
		if current < 0 {
			return nil, errors.New("config entry outside a section")
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !ok || key == "" {
			sections[current].Error = "malformed config entry"
			continue
		}
		if _, ok := sections[current].Values[key]; ok {
			sections[current].Error = "duplicate config key"
		}
		sections[current].Values[key] = strings.TrimSpace(value)
	}
	return sections, s.Err()
}

var accountRE = regexp.MustCompile(`^[0-9]{12}$`)
var roleARNRE = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):iam::([0-9]{12}):role/([A-Za-z0-9+=,.@_/-]+)$`)
var assumedRE = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):sts::([0-9]{12}):assumed-role/([A-Za-z0-9+=,.@_-]+)/([^/\s]+)$`)
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_.@+=,-]+$`)

func Load(path string) ([]domain.Profile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []domain.Profile{}, nil
	}
	if err != nil {
		return nil, err
	}
	sections, err := Parse(data)
	if err != nil {
		return nil, err
	}
	entries := map[string]Section{}
	sessions := map[string]Section{}
	for _, section := range sections {
		if strings.HasPrefix(section.Name, "sso-session ") {
			sessions[strings.TrimPrefix(section.Name, "sso-session ")] = section
			continue
		}
		name := section.Name
		if strings.HasPrefix(name, "profile ") {
			name = strings.TrimPrefix(name, "profile ")
		} else if name != "default" {
			continue
		}
		if old, ok := entries[name]; ok {
			old.Error = "ambiguous profile definition"
			entries[name] = old
			continue
		}
		entries[name] = section
	}
	var resolve func(string, map[string]bool, int) (domain.Profile, error)
	resolve = func(name string, stack map[string]bool, depth int) (domain.Profile, error) {
		p := domain.Profile{Name: name, Partition: "aws"}
		entry, ok := entries[name]
		if !ok {
			return p, fmt.Errorf("source profile %q is missing from AWS config", name)
		}
		if entry.Error != "" {
			return p, errors.New(entry.Error)
		}
		if stack[name] {
			return p, errors.New("source profile cycle")
		}
		if depth >= MaxDepth {
			return p, errors.New("source profile chain exceeds maximum depth")
		}
		stack[name] = true
		defer delete(stack, name)
		v := entry.Values
		p.Region = v["region"]
		for _, provider := range []string{"credential_process", "credential_source", "web_identity_token_file", "aws_access_key_id", "aws_secret_access_key", "login_session"} {
			if v[provider] != "" {
				return p, fmt.Errorf("unsupported provider: %s", provider)
			}
		}
		if v["role_arn"] != "" || v["source_profile"] != "" {
			m := roleARNRE.FindStringSubmatch(v["role_arn"])
			if m == nil || v["source_profile"] == "" {
				return p, errors.New("malformed role ARN or missing source_profile")
			}
			if v["sso_session"] != "" || v["sso_account_id"] != "" {
				return p, errors.New("ambiguous role provider")
			}
			p.AccountID = m[2]
			p.Partition = m[1]
			p.Role = m[3]
			root, err := resolve(v["source_profile"], stack, depth+1)
			p.SSOSession = root.SSOSession
			if p.Region == "" {
				p.Region = root.Region
			}
			if err != nil {
				return p, err
			}
			p.Chain = append(append([]string{}, root.Chain...), name)
			return p, nil
		}
		p.SSOSession = v["sso_session"]
		p.AccountID = v["sso_account_id"]
		p.Role = v["sso_role_name"]
		session, ok := sessions[p.SSOSession]
		if p.SSOSession == "" {
			return p, errors.New("sso_session is missing from this AWS profile")
		}
		if !ok {
			return p, fmt.Errorf("SSO session %q is missing from AWS config; add its [sso-session %s] definition", p.SSOSession, p.SSOSession)
		}
		if session.Error != "" || session.Values["sso_start_url"] == "" || session.Values["sso_region"] == "" {
			return p, fmt.Errorf("SSO session %q is ambiguous or incomplete; check sso_start_url and sso_region", p.SSOSession)
		}
		if !accountRE.MatchString(p.AccountID) || !nameRE.MatchString(p.Role) {
			return p, errors.New("malformed SSO account or permission set")
		}
		region := session.Values["sso_region"]
		if p.Region == "" {
			p.Region = region
		}
		if strings.HasPrefix(region, "cn-") {
			p.Partition = "aws-cn"
		}
		if strings.HasPrefix(region, "us-gov-") {
			p.Partition = "aws-us-gov"
		}
		p.DirectSSO = true
		p.Chain = []string{name}
		return p, nil
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]domain.Profile, 0, len(names))
	for _, name := range names {
		p, err := resolve(name, map[string]bool{}, 0)
		if err != nil {
			p.Error = err.Error()
		}
		out = append(out, p)
	}
	return out, nil
}

// ValidateIdentity checks the exact destination, not a profile display label.
func ValidateIdentity(p domain.Profile, id domain.Identity) error {
	mismatch := errors.New("Connection blocked: the resolved AWS identity does not match this account and role")
	m := assumedRE.FindStringSubmatch(id.ARN)
	if p.Error != "" || m == nil || id.Account != p.AccountID || m[2] != p.AccountID || m[1] != p.Partition {
		return mismatch
	}
	if p.DirectSSO {
		expected := regexp.MustCompile(`^AWSReservedSSO_` + regexp.QuoteMeta(p.Role) + `_[0-9a-fA-F]{16}$`)
		if !expected.MatchString(m[3]) {
			return mismatch
		}
	} else {
		role := p.Role
		if i := strings.LastIndex(role, "/"); i >= 0 {
			role = role[i+1:]
		}
		if role == "" || role != m[3] {
			return mismatch
		}
	}
	return nil
}
