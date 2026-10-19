package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"github.com/charmbracelet/x/ansi"
)

func fieldIDs(f *form) []string {
	var ids []string
	for _, field := range f.fields {
		ids = append(ids, field.id)
	}
	return ids
}

// keyHits returns every hit region that sends key, in drawing order.
func keyHits(m *Model, key string) []hitRegion {
	m.View()
	var out []hitRegion
	for _, h := range m.hits {
		if h.action.kind == "key" && h.action.key == key {
			out = append(out, h)
		}
	}
	return out
}

func TestNewDatabaseAsksTypeFirst(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	f.m.scroll = 3
	key(f.m, "n")
	r := f.m.review
	if r == nil || r.kind != "new-database" || f.m.form != nil {
		t.Fatalf("n opened review %+v form %v", r, f.m.form != nil)
	}
	var keys []string
	for _, c := range f.m.controls() {
		keys = append(keys, c.key)
	}
	if strings.Join(keys, ",") != "p,d,esc" {
		t.Fatalf("picker controls %v", keys)
	}
	content := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{"RDS PostgreSQL (or any TCP tunnel)", "Amazon DocumentDB"} {
		if !strings.Contains(content, want) {
			t.Fatalf("picker lacks %q:\n%s", want, content)
		}
	}
	if footer := f.m.footer(); footer != "↑/↓: type | Enter: choose | p: PostgreSQL | d: DocumentDB | Esc: cancel" {
		t.Fatalf("picker footer %q", footer)
	}
	if !strings.Contains(f.m.View(), "New database connection") {
		t.Fatalf("picker title missing:\n%s", f.m.View())
	}
	for _, digit := range []string{"1", "2", "3", "4", "5", "6"} {
		key(f.m, digit)
		if f.m.screen != databases || f.m.review == nil || f.m.review.kind != "new-database" {
			t.Fatalf("%s left the picker: screen %d review %v", digit, f.m.screen, f.m.review)
		}
	}
	key(f.m, "esc")
	if f.m.review != nil || f.m.form != nil || f.m.scroll != 3 || f.m.screen != databases {
		t.Fatalf("esc: review %v form %v scroll %d", f.m.review, f.m.form != nil, f.m.scroll)
	}

	f.m.screen = ec2
	key(f.m, "n")
	if f.m.review != nil || f.m.form == nil || f.m.form.kind != "ec2" {
		t.Fatalf("EC2 n: review %v form %+v", f.m.review, f.m.form)
	}
}

func TestDatabaseTypePickerMouseMatchesKeys(t *testing.T) {
	isDocumentDBForm := func(f fixture, how string) {
		t.Helper()
		if f.m.review != nil || f.m.form == nil || f.m.form.kind != "db" || f.m.form.engine != engDocumentDB {
			t.Fatalf("%s: review %v form %+v", how, f.m.review, f.m.form)
		}
	}
	open := func() fixture {
		f := setupUI(t)
		f.m.screen = databases
		key(f.m, "n")
		return f
	}
	// The DocumentDB row and the DocumentDB button both send d.
	hits := keyHits(open().m, "d")
	if len(hits) < 2 {
		t.Fatalf("want a DocumentDB row and button, got %d hits", len(hits))
	}
	for i := range hits {
		f := open()
		clickHit(f.m, keyHits(f.m, "d")[i])
		isDocumentDBForm(f, "click")
	}
	f := open()
	key(f.m, "down")
	if f.m.review.choice != 1 {
		t.Fatalf("down choice %d", f.m.review.choice)
	}
	key(f.m, "enter")
	isDocumentDBForm(f, "down+enter")

	f = open()
	key(f.m, "down")
	key(f.m, "up")
	key(f.m, "enter")
	if f.m.form == nil || f.m.form.engine != engPostgres {
		t.Fatalf("up+enter form %+v", f.m.form)
	}
	f = open()
	key(f.m, "d")
	isDocumentDBForm(f, "d")
}

func TestDatabaseTypePickerFits(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 24}, {140, 24}, {100, 20}} {
		f := setupUI(t)
		f.m.screen = databases
		f.m.width, f.m.height = size[0], size[1]
		key(f.m, "n")
		lines := strings.Split(f.m.View(), "\n")
		if len(lines) > size[1] {
			t.Fatalf("%v: %d lines", size, len(lines))
		}
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > size[0] {
				t.Fatalf("%v: line width %d %q", size, w, line)
			}
		}
		view := strings.Join(lines, "\n")
		for _, want := range []string{"RDS PostgreSQL", "DocumentDB"} {
			if !strings.Contains(view, want) {
				t.Fatalf("%v: view lacks %q:\n%s", size, want, view)
			}
		}
		// The footer buttons are drawn last; the lowest p hit is the button.
		hits := keyHits(f.m, "p")
		if len(hits) == 0 {
			t.Fatalf("%v: no PostgreSQL target", size)
		}
		button := hits[0]
		for _, h := range hits {
			if h.y > button.y {
				button = h
			}
		}
		clickHit(f.m, button)
		if f.m.form == nil || f.m.form.engine != engPostgres {
			t.Fatalf("%v: footer click form %+v", size, f.m.form)
		}
	}
}

func TestDocumentDBFormFields(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	key(f.m, "n")
	key(f.m, "d")
	form := f.m.form
	if form == nil || len(form.fields) != 10 {
		t.Fatalf("DocumentDB form %+v", form)
	}
	if got := strings.Join(fieldIDs(form), ","); got != "name,endpoint,region,dbPort,relay,relayRegion,localPort,auth,database,ca" {
		t.Fatalf("DocumentDB field ids %s", got)
	}
	got := []string{form.get("dbPort"), form.get("localPort"), form.get("auth")}
	if strings.Join(got, ",") != "27017,37017,tunnel" {
		t.Fatalf("DocumentDB defaults %v", got)
	}
	for _, field := range form.fields {
		if strings.Contains(strings.ToLower(field.label), "username") {
			t.Fatalf("DocumentDB form shows %q", field.label)
		}
	}
	labels := map[string]string{}
	for _, field := range form.fields {
		labels[field.id] = field.label
	}
	if labels["endpoint"] != "Cluster endpoint (host)" || labels["auth"] != "Auth (tunnel / docdb-iam)" || labels["database"] != "Database (optional, IAM)" {
		t.Fatalf("DocumentDB labels %v", labels)
	}
	fill(f.m, "documents", docdbHost, "us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "docdb-iam", "app_db", "")
	r := f.m.review
	if r == nil || r.kind != "connection" || f.m.form != nil {
		t.Fatalf("submit: status %q review %+v", f.m.status, r)
	}
	if c := r.connection; c.AuthMode != "docdb-iam" || c.Database != "app_db" || c.DBUser != "" || c.DBPort != 27017 || c.LocalPort != 37017 {
		t.Fatalf("submitted connection %+v", c)
	}

	// The PostgreSQL form keeps all eleven fields and its defaults.
	f = setupUI(t)
	f.m.screen = databases
	key(f.m, "n")
	key(f.m, "p")
	form = f.m.form
	if got := strings.Join(fieldIDs(form), ","); got != "name,endpoint,region,dbPort,relay,relayRegion,localPort,auth,database,dbUser,ca" {
		t.Fatalf("PostgreSQL field ids %s", got)
	}
	if got := []string{form.get("dbPort"), form.get("localPort"), form.get("auth")}; strings.Join(got, ",") != "5432,15432,postgres-iam" {
		t.Fatalf("PostgreSQL defaults %v", got)
	}
}

func TestFormsRefuseTheOtherEngine(t *testing.T) {
	const docdbWant = "Enter a DocumentDB cluster endpoint (.docdb.amazonaws.com). For other databases choose RDS PostgreSQL."
	const postgresWant = "This is a DocumentDB endpoint. Press Esc, then n, and choose DocumentDB."
	f := setupUI(t)
	f.m.screen = databases
	key(f.m, "n")
	key(f.m, "d")
	fill(f.m, "documents", "db.example.rds.amazonaws.com", "us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "tunnel", "", "")
	if f.m.status != docdbWant || f.m.review != nil || f.m.form == nil {
		t.Fatalf("DocumentDB form status %q review %v", f.m.status, f.m.review)
	}

	f = setupUI(t)
	f.m.screen = databases
	key(f.m, "n")
	key(f.m, "p")
	fill(f.m, "documents", docdbHost, "us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "tunnel", "", "", "")
	if f.m.status != postgresWant || f.m.review != nil || f.m.form == nil {
		t.Fatalf("PostgreSQL form status %q review %v", f.m.status, f.m.review)
	}
}

func TestDatabaseDraftResumesByEngine(t *testing.T) {
	f := setupUI(t)
	f.m.screen = databases
	key(f.m, "n")
	key(f.m, "d")
	fill(f.m, "documents")
	key(f.m, "esc")
	if f.m.form != nil || f.m.review != nil {
		t.Fatal("esc from the form did not return to the list")
	}
	key(f.m, "n")
	if f.m.review == nil || f.m.review.choice != 1 {
		t.Fatalf("picker does not highlight the DocumentDB draft: %+v", f.m.review)
	}
	key(f.m, "enter")
	if f.m.form == nil || f.m.form.engine != engDocumentDB || f.m.form.get("name") != "documents" || f.m.form.index != 1 {
		t.Fatalf("draft not resumed: %+v", f.m.form)
	}
	key(f.m, "esc")
	key(f.m, "n")
	key(f.m, "p")
	if f.m.form == nil || f.m.form.engine != engPostgres || f.m.form.get("name") != "" || f.m.form.get("dbPort") != "5432" {
		t.Fatalf("other engine resumed the draft: %+v", f.m.form)
	}
	fill(f.m, "app-db")
	key(f.m, "esc")
	key(f.m, "n")
	if f.m.review == nil || f.m.review.choice != 0 {
		t.Fatalf("picker does not highlight the PostgreSQL draft: %+v", f.m.review)
	}
	key(f.m, "p")
	if f.m.form == nil || f.m.form.get("name") != "app-db" {
		t.Fatalf("PostgreSQL draft not resumed: %+v", f.m.form)
	}
}

func TestEditDerivesEngineFromBookmark(t *testing.T) {
	f := setupUI(t)
	manager, err := connections.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	c := docdbBookmark(f)
	c.LastUsedProfile, c.LastUsedRole, c.DBAccountID = "one", "ReadOnlyAccess", c.AccountID
	if err := manager.Save(c); err != nil {
		t.Fatal(err)
	}
	saved, _ := manager.List()
	f.m.services.Connections = manager
	f.m.screen = databases
	finish(f.m, f.m.load())
	key(f.m, "e")
	form := f.m.form
	if f.m.review != nil || form == nil || form.engine != engDocumentDB || form.original == nil {
		t.Fatalf("edit form %+v review %v", form, f.m.review)
	}
	if form.fields[1].label != "Cluster endpoint (host)" || form.get("auth") != "tunnel" {
		t.Fatalf("edit labels %+v", form.fields)
	}
	form.set("name", "documents-renamed")
	key(f.m, "ctrl+s")
	got, err := manager.List()
	if err != nil || len(got) != 1 || got[0].Name != "documents-renamed" || got[0].DBUser != "reader" || got[0].LastUsedProfile != "one" || got[0].LastUsedRole != "ReadOnlyAccess" || got[0].ID != saved[0].ID {
		t.Fatalf("tunnel edit: %+v %v status %q", got, err, f.m.status)
	}

	// Switching the legacy bookmark to IAM login clears its username.
	key(f.m, "e")
	f.m.form.set("auth", "docdb-iam")
	key(f.m, "ctrl+s")
	got, _ = manager.List()
	if len(got) != 1 || got[0].AuthMode != "docdb-iam" || got[0].DBUser != "" || got[0].LastUsedProfile != "one" || got[0].LastUsedRole != "ReadOnlyAccess" {
		t.Fatalf("IAM edit: %+v status %q", got, f.m.status)
	}

	// An RDS bookmark edits in the PostgreSQL form.
	rds := domain.Connection{ID: "rds", Kind: "db", Name: "app", AccountID: c.AccountID, Partition: c.Partition, Region: "us-east-1", Endpoint: "db.example.rds.amazonaws.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: c.RelayInstanceID, RelayRegion: "us-east-1"}
	f.m.editConnection(rds)
	if f.m.form.engine != engPostgres || len(f.m.form.fields) != 11 || f.m.form.fields[1].label != "Endpoint (host)" {
		t.Fatalf("RDS edit form %+v", f.m.form)
	}
}
