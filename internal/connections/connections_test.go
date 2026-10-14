package connections

import (
	"bytes"
	"encoding/json"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ec2() domain.Connection {
	return domain.Connection{ID: "instance-1", Kind: "ec2", Name: "api-01", Partition: "aws", AccountID: "111111111111", Region: "ap-south-1", InstanceID: "i-0123456789abcdef0", LastUsedProfile: "default"}
}
func db() domain.Connection {
	return domain.Connection{ID: "db-1", Kind: "db", Name: "postgres", Partition: "aws", AccountID: "111111111111", Region: "eu-west-1", Endpoint: "db.example.rds.amazonaws.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "ap-south-1"}
}
func jsonDoc(t *testing.T, cs ...domain.Connection) []byte {
	t.Helper()
	b, e := json.Marshal(document{1, cs})
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestRoundTripAndSecretFree(t *testing.T) {
	m, e := New(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []domain.Connection{ec2(), db()} {
		if e = m.Save(c); e != nil {
			t.Fatal(e)
		}
	}
	path := filepath.Join(t.TempDir(), "backup.json")
	if e = m.Export(path); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	for _, word := range []string{"AccessKey", "Secret", "SessionToken", "command"} {
		if bytes.Contains(b, []byte(word)) {
			t.Fatal("secret-bearing export")
		}
	}
	other, e := New(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := other.Preview(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Additions) != 2 || len(p.Unbound) != 1 {
		t.Fatalf("bad preview %+v", p)
	}
	if e = other.Commit(p, false); e != nil {
		t.Fatal(e)
	}
	cs, _ := other.List()
	if cs[1].Region != "eu-west-1" || cs[1].RelayRegion != "ap-south-1" {
		t.Fatal("regions lost")
	}
}
func TestRejectUnsafeImports(t *testing.T) {
	good := string(jsonDoc(t, ec2()))
	cases := []string{strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":2`, 1), strings.Replace(good, `"kind":"ec2"`, `"command":"rm -rf /","kind":"ec2"`, 1), strings.Replace(good, `"id":"instance-1"`, `"id":"instance-1","id":"other"`, 1), good + `{}`, string(jsonDoc(t, ec2(), ec2()))}
	bad := db()
	bad.Endpoint = "https://user:pass@db.example.com"
	cases = append(cases, string(jsonDoc(t, bad)))
	bad = db()
	bad.DBAccountID = "222222222222"
	cases = append(cases, string(jsonDoc(t, bad)))
	bad = db()
	bad.LocalPort = 80
	cases = append(cases, string(jsonDoc(t, bad)))
	c := ec2()
	c.Name = "\x1b[31mmalicious"
	cases = append(cases, string(jsonDoc(t, c)))
	c = ec2()
	c.LastUsedAt = new(time.Time)
	cases = append(cases, strings.Replace(string(jsonDoc(t, c)), "0001-01-01T00:00:00Z", "yesterday", 1))
	cases = append(cases, strings.Repeat(" ", MaxBytes+1))
	for i, b := range cases {
		if _, e := decode([]byte(b)); e == nil {
			t.Errorf("unsafe import %d accepted", i)
		}
	}
}
func TestImportPreviewConflictAndConcurrentEdit(t *testing.T) {
	m, _ := New(filepath.Join(t.TempDir(), "state"))
	c := ec2()
	if e := m.Save(c); e != nil {
		t.Fatal(e)
	}
	c.Name = "incoming"
	f := filepath.Join(t.TempDir(), "import.json")
	os.WriteFile(f, jsonDoc(t, c), 0600)
	p, e := m.Preview(f)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Conflicts) != 1 {
		t.Fatal("expected conflict")
	}
	if e = m.Commit(p, false); e != nil {
		t.Fatal(e)
	}
	cs, _ := m.List()
	if cs[0].Name != "api-01" {
		t.Fatal("default overwrote local")
	}
	p, _ = m.Preview(f)
	if e = m.Commit(p, true); e != nil {
		t.Fatal(e)
	}
	cs, _ = m.List()
	if cs[0].Name != "incoming" {
		t.Fatal("explicit replacement ignored")
	}
	p, _ = m.Preview(f)
	if e = m.Save(db()); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(m.Path())
	if e = m.Commit(p, true); e == nil {
		t.Fatal("concurrent edit accepted")
	}
	after, _ := os.ReadFile(m.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("failed import changed file")
	}
}
func TestSaveDedupAndNormalization(t *testing.T) {
	m, _ := New(filepath.Join(t.TempDir(), "state"))
	c := db()
	c.Endpoint = strings.ToUpper(c.Endpoint)
	if e := m.Save(c); e != nil {
		t.Fatal(e)
	}
	c.ID = "different-id"
	c.LocalPort = 25432
	if e := m.Save(c); e != nil {
		t.Fatal(e)
	}
	cs, _ := m.List()
	if len(cs) != 1 || cs[0].ID != "db-1" || cs[0].LocalPort != 25432 || cs[0].Endpoint != strings.ToLower(c.Endpoint) {
		t.Fatal(cs)
	}
}
func TestRecordLimitAndPartitionCompatibility(t *testing.T) {
	cs := make([]domain.Connection, 10001)
	for i := range cs {
		cs[i] = ec2()
	}
	if _, e := decode(jsonDoc(t, cs...)); e == nil {
		t.Fatal("record limit ignored")
	}
	for _, x := range []struct {
		p, r string
		want bool
	}{{"aws", "cn-north-1", false}, {"aws-cn", "cn-north-1", true}, {"aws-us-gov", "us-gov-west-1", true}, {"aws", "us-east-1", true}} {
		if CompatibleRegion(x.p, x.r) != x.want {
			t.Fatal(x)
		}
	}
}
func TestExportCannotOverwriteCredentialsOrApplicationState(t *testing.T) {
	m, e := New(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	existing := filepath.Join(t.TempDir(), "important")
	if e = os.WriteFile(existing, []byte("preserve"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = m.Export(existing); e == nil {
		t.Fatal("existing unrelated file overwritten")
	}
	b, _ := os.ReadFile(existing)
	if string(b) != "preserve" {
		t.Fatal("existing file lost")
	}
	absentCreds := filepath.Join(t.TempDir(), "credentials")
	m.SetProtectedPaths(absentCreds)
	for _, p := range []string{absentCreds, m.Path(), filepath.Join(filepath.Dir(m.Path()), "settings.json"), filepath.Join(filepath.Dir(m.Path()), "runtime", "run.json")} {
		if e = m.Export(p); e == nil {
			t.Fatal("reserved path accepted", p)
		}
	}
}

func TestUpdateRejectsCollisionAndMissingBookmark(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	a, b := ec2(), ec2()
	b.ID = "instance-2"
	b.InstanceID = "i-1123456789abcdef0"
	for _, c := range []domain.Connection{a, b} {
		if err := m.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	edited := a
	edited.InstanceID = b.InstanceID
	if err := m.Update(edited); err == nil {
		t.Fatal("destination collision accepted")
	}
	edited = a
	edited.ID = "missing"
	if err := m.Update(edited); err == nil {
		t.Fatal("missing record created")
	}
	edited = a
	edited.AccountID = "222222222222"
	if err := m.Update(edited); err == nil {
		t.Fatal("account changed")
	}
	got, err := m.List()
	if err != nil || len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatal("failed edit changed saved records")
	}
}
