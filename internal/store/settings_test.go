package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAliasesValidateLoadAndSave(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	if e := EnsureDir(d); e != nil {
		t.Fatal(e)
	}
	if e := SaveAlias(d, "111111111111", "My account"); e != nil {
		t.Fatal(e)
	}
	s, e := LoadSettings(d)
	if e != nil || s.AccountAliases["111111111111"] != "My account" {
		t.Fatal(s, e)
	}
	if e = SaveAlias(d, "111111111111", "bad\x1b"); e == nil {
		t.Fatal("control accepted")
	}
	os.WriteFile(filepath.Join(d, "settings.json"), []byte(`{"version":1,"accountAliases":{"111111111111":"\u001b[31m"}}`), 0600)
	if _, e = LoadSettings(d); e == nil {
		t.Fatal("unsafe stored alias accepted")
	}
}

func TestAliasesHaveNoOrganizationDefaults(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	if err := EnsureDir(d); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(d)
	if err != nil || len(s.AccountAliases) != 0 {
		t.Fatalf("fresh settings contain invented aliases: %+v, %v", s, err)
	}
	if err := SaveAlias(d, "111122223333", "My configured account"); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSettings(d)
	if err != nil || len(s.AccountAliases) != 1 || s.AccountAliases["111122223333"] != "My configured account" {
		t.Fatalf("explicit user alias not preserved: %+v, %v", s, err)
	}
}

func TestThemePreservesAccountAliases(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	if err := EnsureDir(d); err != nil {
		t.Fatal(err)
	}
	if err := SaveAlias(d, "111122223333", "Team"); err != nil {
		t.Fatal(err)
	}
	if err := SaveTheme(d, "dark"); err != nil {
		t.Fatal(err)
	}
	if err := SaveAlias(d, "111122223333", "Production"); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(d)
	if err != nil || s.Theme != "dark" || s.AccountAliases["111122223333"] != "Production" {
		t.Fatal(s, err)
	}
	if SaveTheme(d, "invalid") == nil {
		t.Fatal("invalid theme accepted")
	}
}
