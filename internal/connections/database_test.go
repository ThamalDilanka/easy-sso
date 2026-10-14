package connections

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIAMBookmarksRoundTripAndLegacyCompatibility(t *testing.T) {
	legacy := db()
	if legacy.IAMDatabase() {
		t.Fatal("old bookmark migrated implicitly")
	}
	iam := db()
	iam.ID = "iam"
	iam.AuthMode = "postgres-iam"
	iam.Database = "exampledb"
	iam.DBUser = "reader"
	iam.CAFile = "/custom/ca.pem"
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ user, id string }{{"reader", "iam"}, {"other", "other"}} {
		copy := iam
		copy.DBUser = c.user
		copy.ID = c.id
		if err = m.Save(copy); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.Save(legacy); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	if err = m.Export(path); err != nil {
		t.Fatal(err)
	}
	other, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := other.Preview(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Commit(plan, false); err != nil {
		t.Fatal(err)
	}
	got, _ := other.List()
	if len(got) != 3 || !reflect.DeepEqual(got[0], iam) || got[2].IAMDatabase() {
		t.Fatal("login configuration changed on roundtrip")
	}
	raw, _ := os.ReadFile(path)
	for _, field := range []string{"password", "token", "snapshot"} {
		unsafe := strings.Replace(string(raw), `"authMode": "postgres-iam"`, `"authMode": "postgres-iam", "`+field+`": "secret"`, 1)
		if _, err = decode([]byte(unsafe)); err == nil {
			t.Fatal("accepted persisted credential", field)
		}
	}
	iam.DBUser = ""
	if err = Validate(iam); err == nil {
		t.Fatal("IAM bookmark without user accepted")
	}
}
