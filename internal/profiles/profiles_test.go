package profiles

import (
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadText(t *testing.T, text string) []domain.Profile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const rootConfig = "[sso-session example]\nsso_start_url = https://example.awsapps.com/start\nsso_region = eu-west-1\n[profile readonly]\nsso_session = example\nsso_account_id = 111111111111\nsso_role_name = ReadOnlyAccess\nregion = eu-west-1\n"

func TestChainsAndDestinationIdentity(t *testing.T) {
	out := loadText(t, rootConfig+"[profile linked-readonly]\nsource_profile=readonly\nrole_arn=arn:aws:iam::222222222222:role/path/OrganizationAccountAccessRole\n[profile third]\nsource_profile=linked-readonly\nrole_arn=arn:aws:iam::111122223333:role/FinalRole\n")
	for _, p := range out {
		if p.Error != "" {
			t.Fatal(p.Error)
		}
		if p.Name == "third" && (p.AccountID != "111122223333" || p.Role != "FinalRole" || p.SSOSession != "example" || len(p.Chain) != 3 || p.Region != "eu-west-1") {
			t.Fatalf("wrong destination: %+v", p)
		}
	}
}
func TestUnavailableProfiles(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"cycle", "[profile a]\nsource_profile=b\nrole_arn=arn:aws:iam::111122223333:role/R\n[profile b]\nsource_profile=a\nrole_arn=arn:aws:iam::111122223333:role/R\n"},
		{"missing", "[profile a]\nsource_profile=missing\nrole_arn=arn:aws:iam::111122223333:role/R\n"},
		{"malformed", "[profile a]\nsource_profile=readonly\nrole_arn=bad\n"},
		{"process", "[profile a]\ncredential_process=sh -c should-not-run\n"},
		{"duplicates", "[default]\nsso_session=example\n[profile default]\nsso_session=example\n"},
		{"duplicate key", "[profile a]\nsso_session=example\nsso_session=other\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := loadText(t, tc.text)
			for _, p := range out {
				if p.Error == "" {
					t.Fatalf("unsupported profile accepted %+v", p)
				}
			}
		})
	}
}
func TestMaximumDepth(t *testing.T) {
	text := rootConfig
	for i := 0; i < MaxDepth+2; i++ {
		source := "readonly"
		if i > 0 {
			source = fmt.Sprintf("p%d", i-1)
		}
		text += fmt.Sprintf("[profile p%d]\nsource_profile=%s\nrole_arn=arn:aws:iam::111122223333:role/R\n", i, source)
	}
	out := loadText(t, text)
	for _, p := range out {
		if p.Name == fmt.Sprintf("p%d", MaxDepth+1) && !strings.Contains(p.Error, "depth") {
			t.Fatal(p)
		}
	}
}
func TestExactRoleVerification(t *testing.T) {
	p := domain.Profile{AccountID: "111111111111", Partition: "aws", Role: "ReadOnlyAccess", DirectSSO: true}
	valid := "arn:aws:sts::111111111111:assumed-role/AWSReservedSSO_ReadOnlyAccess_0123456789abcdef/user"
	for _, tc := range []struct {
		arn, account string
		valid        bool
	}{{valid, p.AccountID, true}, {strings.Replace(valid, "ReadOnlyAccess_", "ReadOnlyAccessExtra_", 1), p.AccountID, false}, {valid + "/extra", p.AccountID, false}, {strings.Replace(valid, "0123456789abcdef", "suffix", 1), p.AccountID, false}, {valid, "111122223333", false}, {strings.Replace(valid, "arn:aws:", "arn:aws-cn:", 1), p.AccountID, false}} {
		if got := ValidateIdentity(p, domain.Identity{Account: tc.account, ARN: tc.arn}) == nil; got != tc.valid {
			t.Errorf("validation %s got %v", tc.arn, got)
		}
	}
	p.DirectSSO = false
	p.Role = "path/OrganizationAccountAccessRole"
	if err := ValidateIdentity(p, domain.Identity{Account: p.AccountID, ARN: "arn:aws:sts::111111111111:assumed-role/OrganizationAccountAccessRole/user"}); err != nil {
		t.Fatal(err)
	}
}
func TestConfiguredRegionsAreInferredWithoutDefaults(t *testing.T) {
	text := "[sso-session arbitrary-team]\nsso_start_url=https://team.example.awsapps.com/start\nsso_region=ap-southeast-2\n[profile custom-root]\nsso_session=arbitrary-team\nsso_account_id=111122223333\nsso_role_name=CustomRead\n[profile custom-chain]\nsource_profile=custom-root\nrole_arn=arn:aws:iam::444455556666:role/CustomRole\n[profile explicit-region]\nsso_session=arbitrary-team\nsso_account_id=111122223333\nsso_role_name=CustomRead\nregion=eu-central-1\n"
	out := loadText(t, text)
	if len(out) != 3 {
		t.Fatalf("profiles invented or omitted: %+v", out)
	}
	for _, p := range out {
		want := "ap-southeast-2"
		if p.Name == "explicit-region" {
			want = "eu-central-1"
		}
		if p.Error != "" || p.Region != want || p.SSOSession != "arbitrary-team" {
			t.Fatalf("config-derived metadata incorrect: %+v", p)
		}
	}
}

func TestMissingSourcesRetainConfiguredDestination(t *testing.T) {
	for _, tc := range []struct {
		name, text, account, role, missing string
	}{
		{"source", "[profile configured-role]\nsource_profile=not-configured\nrole_arn=arn:aws:iam::444455556666:role/CustomRole\nregion=eu-west-2\n", "444455556666", "CustomRole", "not-configured"},
		{"SSO session", "[profile configured-role]\nsso_session=not-configured\nsso_account_id=111122223333\nsso_role_name=CustomRead\nregion=eu-west-2\n", "111122223333", "CustomRead", "not-configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := loadText(t, tc.text)
			if len(out) != 1 || out[0].AccountID != tc.account || out[0].Role != tc.role || out[0].Region != "eu-west-2" || !strings.Contains(out[0].Error, tc.missing) {
				t.Fatalf("missing config was invented or destination lost: %+v", out)
			}
		})
	}
}

func TestSyntheticConfig(t *testing.T) {
	ps, err := Load("../../testdata/aws-config.ini")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		account, role, source, region string
		depth                         int
	}{
		"default":         {"111111111111", "ReadOnlyAccess", "example", "us-east-1", 1},
		"admin":           {"111111111111", "AdministratorAccess", "example", "us-west-2", 1},
		"linked-readonly": {"222222222222", "ExampleOperator", "example", "us-east-1", 2},
		"sandbox":         {"444455556666", "ReadOnlyAccess", "sandbox", "eu-west-1", 1},
	}
	if len(ps) != len(want) {
		t.Fatalf("got %d profiles, want %d", len(ps), len(want))
	}
	for _, p := range ps {
		w, ok := want[p.Name]
		if !ok || p.Error != "" || p.AccountID != w.account || p.Role != w.role || p.SSOSession != w.source || p.Region != w.region || len(p.Chain) != w.depth {
			t.Fatalf("incorrect synthetic profile resolution: %+v", p)
		}
		delete(want, p.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing profiles: %v", want)
	}
}
