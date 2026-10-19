package ui

import (
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

const docdbHost = "example-cluster.cluster-c0example.us-east-1.docdb.amazonaws.com"

func docdbBookmark(f fixture) domain.Connection {
	p, _ := f.m.profile()
	return domain.Connection{ID: "docdb", Kind: "db", Name: "documents", AccountID: p.AccountID, Partition: p.Partition, Region: "us-east-1", Endpoint: docdbHost, DBPort: 27017, LocalPort: 37017, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1", DBUser: "reader"}
}

// newDatabaseForm opens a new database form with the endpoint field focused
// and holding host.
func newDatabaseForm(t *testing.T, f fixture, host string) {
	t.Helper()
	f.m.screen = databases
	key(f.m, "n")
	fill(f.m, "documents")
	if f.m.form == nil || f.m.form.index != 1 {
		t.Fatal("endpoint field not focused")
	}
	key(f.m, "ctrl+u")
	key(f.m, host)
}

func TestNewDocumentDBFormDefaults(t *testing.T) {
	const status = "DocumentDB endpoint: ports set to 27017/37017, auth set to tunnel"
	leave := map[string]func(fixture){
		"enter": func(f fixture) { key(f.m, "enter") },
		"tab":   func(f fixture) { key(f.m, "tab") },
		"down":  func(f fixture) { key(f.m, "down") },
		"up":    func(f fixture) { key(f.m, "up") },
		"click": func(f fixture) { clickHit(f.m, mouseHit(t, f.m, "field", "", 3)) },
	}
	for name, leaveField := range leave {
		f := setupUI(t)
		newDatabaseForm(t, f, strings.ToUpper(docdbHost)+" ")
		leaveField(f)
		got := []string{f.m.form.fields[3].value, f.m.form.fields[6].value, f.m.form.fields[7].value}
		if strings.Join(got, ",") != "27017,37017,tunnel" || f.m.status != status {
			t.Fatalf("%s: fields %v status %q", name, got, f.m.status)
		}
		content := f.m.content()
		if strings.Contains(content, "rds_iam") || !strings.Contains(content, "DocumentDB user stays in your client") {
			t.Fatalf("%s: form guidance not DocumentDB-specific:\n%s", name, content)
		}
	}

	// A value the user typed is kept; only untouched defaults switch.
	f := setupUI(t)
	newDatabaseForm(t, f, docdbHost)
	f.m.form.fields[3].value = "27018"
	key(f.m, "enter")
	if f.m.form.fields[3].value != "27018" || f.m.form.fields[6].value != "37017" {
		t.Fatalf("typed port replaced: %+v", f.m.form.fields)
	}

	// Leaving again with nothing to change keeps the status quiet.
	f.m.status = ""
	key(f.m, "up")
	key(f.m, "down")
	if f.m.status != "" {
		t.Fatalf("status repeated: %q", f.m.status)
	}

	f = setupUI(t)
	newDatabaseForm(t, f, "db.example.rds.amazonaws.com")
	key(f.m, "enter")
	if f.m.form.fields[3].value != "5432" || f.m.form.fields[6].value != "15432" || f.m.form.fields[7].value != "postgres-iam" {
		t.Fatalf("RDS defaults changed: %+v", f.m.form.fields)
	}

	// Editing a saved bookmark never rewrites its values.
	f = setupUI(t)
	f.m.screen = databases
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort, rds.DBUser = "db.example.rds.amazonaws.com", 5432, 15432, ""
	f.m.editConnection(rds)
	f.m.form.index = 1
	key(f.m, "ctrl+u")
	key(f.m, docdbHost)
	key(f.m, "enter")
	if f.m.form.fields[3].value != "5432" || f.m.form.fields[6].value != "15432" {
		t.Fatalf("edit form rewritten: %+v", f.m.form.fields)
	}
}

func TestDocumentDBSubmitRejectsPostgresIAM(t *testing.T) {
	const want = "PostgreSQL IAM login does not apply to DocumentDB; set Auth to tunnel"
	f := setupUI(t)
	newDatabaseForm(t, f, docdbHost)
	key(f.m, "enter")
	// Database name and user stay blank: the DocumentDB message wins.
	fill(f.m, "us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "postgres-iam", "", "", "")
	if f.m.status != want || f.m.review != nil || f.m.form == nil {
		t.Fatalf("submit status %q review %v", f.m.status, f.m.review)
	}

	// A legacy bookmark on disk is refused before anything opens.
	f = setupUI(t)
	c := docdbBookmark(f)
	c.AuthMode, c.Database = "postgres-iam", "exampledb"
	if cmd := f.m.open(c); cmd != nil || f.m.status != want || len(f.sessions.opened) != 0 {
		t.Fatalf("open status %q opened %d", f.m.status, len(f.sessions.opened))
	}
}

func TestDocumentDBHidesIAMSetup(t *testing.T) {
	const status = "IAM login setup is for PostgreSQL; DocumentDB uses your own user in the client."
	f := setupUI(t)
	f.m.screen = databases
	f.m.connections = []domain.Connection{docdbBookmark(f)}
	noIAMControl := func(where string) {
		t.Helper()
		for _, c := range f.m.controls() {
			if c.key == "l" {
				t.Fatalf("%s offers %q", where, c.label)
			}
		}
	}
	noIAMControl("databases list")
	key(f.m, "l")
	if f.m.form != nil || f.m.status != status {
		t.Fatalf("list l: form %v status %q", f.m.form != nil, f.m.status)
	}
	key(f.m, "v")
	if f.m.review == nil || f.m.review.kind != "connection" {
		t.Fatal("details review not open")
	}
	noIAMControl("connection review")
	content := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{"Engine", "DocumentDB", "Tunnel only · your DocumentDB user stays in your client"} {
		if !strings.Contains(content, want) {
			t.Fatalf("review lacks %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"rds_iam", "Set up IAM", "PostgreSQL IAM"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("review shows %q:\n%s", unwanted, content)
		}
	}
	f.m.status = ""
	key(f.m, "l")
	if f.m.form != nil || f.m.status != status {
		t.Fatalf("review l: form %v status %q", f.m.form != nil, f.m.status)
	}

	// The list panel and the compact layout say the same.
	key(f.m, "esc")
	for _, height := range []int{24, 40} {
		f.m.height = height
		if view := f.m.View(); strings.Contains(view, "PostgreSQL IAM") || !strings.Contains(view, "DocumentDB") {
			t.Fatalf("list panel at height %d:\n%s", height, view)
		}
	}

	// An RDS tunnel keeps its engine label and IAM setup.
	rds := docdbBookmark(f)
	rds.Endpoint, rds.DBPort, rds.LocalPort = "db.example.rds.amazonaws.com", 5432, 15432
	f.m.connections = []domain.Connection{rds}
	f.m.connectionReview(rds)
	if content := strings.Join(f.m.reviewContent(), "\n"); !strings.Contains(content, "PostgreSQL / other") || !strings.Contains(content, "Set up IAM login (l)") {
		t.Fatalf("RDS review changed:\n%s", content)
	}
}

func TestEmptyStatesMentionDocumentDB(t *testing.T) {
	f := setupUI(t)
	for _, screen := range []screen{databases, tunnels} {
		f.m.screen = screen
		f.m.connections, f.m.runs = nil, nil
		if content := f.m.content(); !strings.Contains(content, "DocumentDB") {
			t.Fatalf("empty state of screen %d lacks DocumentDB:\n%s", screen, content)
		}
	}
}

func TestHelpMentionsDocumentDB(t *testing.T) {
	f := setupUI(t)
	if help := f.m.helpContent(); !strings.Contains(help, "DocumentDB") {
		t.Fatalf("help lacks DocumentDB:\n%s", help)
	}
}
