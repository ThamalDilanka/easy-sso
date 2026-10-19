package ui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/ThamalDilanka/easy-sso/internal/profiles"
	tea "github.com/charmbracelet/bubbletea"
)

const dynamicConfig = "[sso-session custom-login]\nsso_start_url=https://custom.example.awsapps.com/start\nsso_region=eu-west-1\n[profile selected]\nsso_session=custom-login\nsso_account_id=111122223333\nsso_role_name=CustomRead\nregion=eu-west-1\n"

func writeDynamicConfig(t *testing.T, path, config string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDynamicConfigReloadAddEditRemoveAndRecovery(t *testing.T) {
	f := setupUI(t)
	path := filepath.Join(t.TempDir(), "config")
	writeDynamicConfig(t, path, dynamicConfig)
	f.m.services.LoadProfiles = func() ([]domain.Profile, error) { return profiles.Load(path) }
	finish(f.m, f.m.refreshProfiles())
	if len(f.m.services.Profiles) != 1 || f.m.selectedProfile != -1 {
		t.Fatalf("new config must require explicit selection: %+v", f.m.services.Profiles)
	}
	f.m.changeProfile(0)
	p, _ := f.m.profile()
	frozen := domain.Run{ID: "frozen", Profile: p, State: "Connected", Alias: "tui-selected", Connection: domain.Connection{Kind: "ec2", AccountID: p.AccountID}}
	f.sessions.runs = []domain.Run{frozen}
	finish(f.m, f.m.load())
	added := dynamicConfig + "[profile added]\nsso_session=custom-login\nsso_account_id=000011112222\nsso_role_name=Other\nregion=ap-south-1\n"
	writeDynamicConfig(t, path, added)
	finish(f.m, f.m.refreshProfiles())
	selected, ok := f.m.profile()
	if !ok || selected.Name != "selected" || f.m.selectedProfile != 1 {
		t.Fatalf("selection changed after insertion/reordering: %+v", selected)
	}
	f.m.form = &form{kind: "ec2"}
	f.m.review = &review{kind: "connection"}
	f.m.picker = true
	f.m.picked[0] = true
	f.m.drafts[ec2] = form{kind: "ec2"}
	epoch := f.m.epoch
	writeDynamicConfig(t, path, strings.ReplaceAll(added, "CustomRead", "ChangedRole"))
	finish(f.m, f.m.refreshProfiles())
	selected, _ = f.m.profile()
	if selected.Role != "ChangedRole" || f.m.epoch <= epoch || f.m.form != nil || f.m.review != nil || f.m.picker || len(f.m.picked) != 0 || len(f.m.drafts) != 0 {
		t.Fatal("identity edit did not invalidate pending approvals and drafts")
	}
	if !reflect.DeepEqual(f.m.runs[0], frozen) || !reflect.DeepEqual(f.sessions.runs[0], frozen) {
		t.Fatal("config edit retargeted an existing run")
	}
	writeDynamicConfig(t, path, strings.ReplaceAll(dynamicConfig, "selected", "replacement"))
	finish(f.m, f.m.refreshProfiles())
	if f.m.selectedProfile != -1 {
		t.Fatal("removed selection silently switched to another profile")
	}
	writeDynamicConfig(t, path, "region=us-east-1\n")
	finish(f.m, f.m.refreshProfiles())
	if len(f.m.services.Profiles) != 0 || !strings.Contains(f.m.status, "reload failed") {
		t.Fatal("malformed config retained stale connectable profiles")
	}
	writeDynamicConfig(t, path, dynamicConfig)
	finish(f.m, f.m.refreshProfiles())
	if len(f.m.services.Profiles) != 1 {
		t.Fatal("fixed config did not recover")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	finish(f.m, f.m.refreshProfiles())
	if len(f.m.services.Profiles) != 0 || !reflect.DeepEqual(f.m.runs[0], frozen) {
		t.Fatal("missing config must disable new profile actions and retain runs")
	}
}

func TestDynamicReloadDefersDuringOperationAndRejectsStaleResult(t *testing.T) {
	f := setupUI(t)
	original := f.m.services.Profiles[0]
	updated := original
	updated.Role = "ChangedRole"
	calls := 0
	f.m.services.LoadProfiles = func() ([]domain.Profile, error) {
		calls++
		return []domain.Profile{updated}, nil
	}
	refresh := f.m.refreshProfiles()
	operation := f.m.operation("Import preview", func(context.Context) (domain.Run, domain.ImportPlan, error) {
		return domain.Run{}, domain.ImportPlan{Additions: []domain.Connection{{ID: "stale"}}}, nil
	})
	finish(f.m, refresh)
	if f.m.services.Profiles[0].Role != original.Role || f.m.pendingProfiles == nil {
		t.Fatal("config reload applied during active operation")
	}
	if f.m.refreshProfiles() != nil || calls != 1 {
		t.Fatal("busy operation launched another reload")
	}
	_, next := f.m.Update(operation())
	if next != nil {
		if batch, ok := next().(tea.BatchMsg); ok {
			for _, command := range batch {
				finish(f.m, command)
			}
		}
	}
	if f.m.services.Profiles[0].Role != updated.Role || f.m.review != nil || f.m.pendingProfiles != nil {
		t.Fatal("deferred reload enabled a stale async approval")
	}
	if calls != 2 {
		t.Fatal("profile refresh did not resume after operation")
	}
}

func TestManualReloadAndRenewalUseCurrentProfile(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	role := domain.ManagedRole{Profile: p, Alias: "tui-one"}
	p.Role = "CurrentRole"
	loads := 0
	f.m.services.LoadProfiles = func() ([]domain.Profile, error) {
		loads++
		return []domain.Profile{p}, nil
	}
	_, cmd := f.m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	finish(f.m, cmd)
	if loads != 1 {
		t.Fatal("Ctrl+R did not reread config")
	}
	f.m.renewRole(role)
	if f.m.review == nil || f.m.review.selections[0].Profile.Role != "CurrentRole" {
		t.Fatal("renewal used stale managed identity instead of current config review")
	}
	f.m.form, f.m.review = nil, nil
	f.m.applyProfiles(profilesMsg{})
	f.m.renewRole(role)
	if f.m.review != nil || !strings.Contains(f.m.status, "no longer configured") {
		t.Fatal("removed source profile allowed renewal")
	}
}

func TestConnectionApprovalRejectsConfigEditedBeforeRefresh(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	p := f.m.services.Profiles[0]
	changed := p
	changed.Role = "UnreviewedRole"
	f.m.services.LoadProfiles = func() ([]domain.Profile, error) { return []domain.Profile{changed}, nil }
	c := domain.Connection{Kind: "ec2", AccountID: p.AccountID, Partition: p.Partition, Region: p.Region, InstanceID: "i-0123456789abcdef0"}
	finish(f.m, f.m.open(c))
	if len(f.sessions.opened) != 0 || f.creds.snapshots != 0 || !strings.Contains(f.m.status, "changed after review") {
		t.Fatal("profile edited before polling interval was allowed to open")
	}
}

func TestUnrelatedPublishedProfilePreservesApprovalAndOpenResult(t *testing.T) {
	f := setupUI(t)
	original := f.m.services.Profiles[0]
	updated := append([]domain.Profile{}, f.m.services.Profiles...)
	updated = append(updated, domain.Profile{Name: "tui-one", Region: original.Region, Error: "unsupported provider"})
	f.m.services.LoadProfiles = func() ([]domain.Profile, error) { return updated, nil }
	f.m.review = &review{kind: "connection"}
	f.m.drafts[ec2] = form{kind: "ec2", fields: []field{{label: "Name", value: "Keep draft", id: "name"}}}
	epoch := f.m.epoch
	refresh := f.m.refreshProfiles()
	operation := f.m.operation("Open connection", func(context.Context) (domain.Run, domain.ImportPlan, error) {
		return domain.Run{State: "Listening", Connection: domain.Connection{Kind: "db", LocalPort: 15432}}, domain.ImportPlan{}, nil
	})
	finish(f.m, refresh)
	finish(f.m, operation)
	selected, ok := f.m.profile()
	if !ok || selected.Name != original.Name || f.m.epoch != epoch || len(f.m.drafts) != 1 || !strings.Contains(f.m.status, "Tunnel open at localhost:15432") {
		t.Fatalf("unrelated published alias invalidated successful run: %s", f.m.status)
	}
}

func TestActivationReviewInvalidatesOnlyChangedSelectedDefinitions(t *testing.T) {
	f := setupUI(t)
	selected := f.m.services.Profiles[0]
	other := f.m.services.Profiles[1]
	f.m.review = &review{kind: "activation", selections: []domain.Selection{{Profile: other, Alias: "tui-two"}}}
	f.m.form = &form{kind: "activation"}
	changedOther := other
	changedOther.Role = "Changed"
	f.m.applyProfiles(profilesMsg{profiles: []domain.Profile{selected, changedOther}})
	if f.m.review != nil || f.m.form != nil {
		t.Fatal("changed multi-role activation definition retained stale approval")
	}
}
