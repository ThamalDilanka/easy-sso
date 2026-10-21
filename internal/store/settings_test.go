package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestClientPlacesValidateAndPersist(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	if err := EnsureDir(d); err != nil {
		t.Fatal(err)
	}
	if err := SaveAlias(d, "111122223333", "Team"); err != nil {
		t.Fatal(err)
	}
	if err := SaveClientPlace(d, "datagrip", "windows"); err != nil {
		t.Fatal(err)
	}
	if err := SaveClientPlace(d, "compass", "local"); err != nil {
		t.Fatal(err)
	}
	if err := SaveTheme(d, "dark"); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSettings(d)
	if err != nil || s.ClientPlaces["datagrip"] != "windows" || s.ClientPlaces["compass"] != "local" || s.Theme != "dark" || s.AccountAliases["111122223333"] != "Team" {
		t.Fatalf("settings %+v, %v", s, err)
	}
	if err := SaveClientPlace(d, "datagrip", "local"); err != nil {
		t.Fatal(err)
	}
	if s, _ = LoadSettings(d); s.ClientPlaces["datagrip"] != "local" {
		t.Fatalf("choice not replaced: %+v", s.ClientPlaces)
	}
	for _, bad := range [][2]string{{"intellij", "local"}, {"DataGrip", "local"}, {"datagrip", "mac"}, {"compass", ""}, {"", "windows"}} {
		if SaveClientPlace(d, bad[0], bad[1]) == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// A stored entry this build does not know is dropped, never fatal.
	os.WriteFile(filepath.Join(d, "settings.json"), []byte(`{"version":1,"accountAliases":{},"clientPlaces":{"datagrip":"windows","intellij":"local","compass":"\u001b[31m"}}`), 0600)
	s, err = LoadSettings(d)
	if err != nil || len(s.ClientPlaces) != 1 || s.ClientPlaces["datagrip"] != "windows" {
		t.Fatalf("unknown entries kept or fatal: %+v, %v", s.ClientPlaces, err)
	}
	// Older files have no field; older builds ignore it on a plain Unmarshal.
	os.WriteFile(filepath.Join(d, "settings.json"), []byte(`{"version":1,"accountAliases":{}}`), 0600)
	if s, err = LoadSettings(d); err != nil || len(s.ClientPlaces) != 0 {
		t.Fatalf("old file: %+v, %v", s, err)
	}
	if err := SaveTheme(d, "light"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(d, "settings.json")); strings.Contains(string(b), "clientPlaces") {
		t.Fatalf("empty choice written: %s", b)
	}
	var older struct {
		Version        int               `json:"version"`
		AccountAliases map[string]string `json:"accountAliases"`
	}
	if err := json.Unmarshal([]byte(`{"version":1,"accountAliases":{},"clientPlaces":{"datagrip":"windows"}}`), &older); err != nil || older.Version != 1 {
		t.Fatalf("older build could not read the file: %v", err)
	}
}
