package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

func TestEditDatabaseTunnelDocument(t *testing.T) {
	f := setupUI(t)
	manager, err := connections.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Connection{ID: "saved", Kind: "db", Name: "db", AccountID: "111111111111", Partition: "aws", Region: "us-east-1", Endpoint: "db.example.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1", DBAccountID: "111111111111"}
	if err = manager.Save(c); err != nil {
		t.Fatal(err)
	}
	f.m.services.Connections = manager
	f.m.screen = databases
	finish(f.m, f.m.load())
	key(f.m, "e")
	f.m.form.set("document", "  Example-TunnelDocument  ")
	key(f.m, "ctrl+s")
	got, _ := manager.List()
	if len(got) != 1 || got[0].DocumentName != "Example-TunnelDocument" {
		t.Fatalf("tunnel document not saved: %+v (%s)", got, f.m.status)
	}
	key(f.m, "e")
	if f.m.form.get("document") != "Example-TunnelDocument" {
		t.Fatal("edit form lost the tunnel document")
	}
	f.m.form.set("document", "SSM-SessionManagerRunShell")
	key(f.m, "ctrl+s")
	if f.m.form == nil || !strings.Contains(f.m.status, "not a remote-host port forwarding document") {
		t.Fatalf("shell document accepted for a tunnel: %s", f.m.status)
	}
}

func TestDetailsShowTunnelDocument(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	c := domain.Connection{Kind: "db", Name: "db", Partition: "aws", AccountID: p.AccountID, Region: "us-east-1", Endpoint: "db.example.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1"}
	if got := strings.Join(f.m.connectionDetails(c, p, true), "\n"); !strings.Contains(got, "AWS-StartPortForwardingSessionToRemoteHost (default)") {
		t.Fatalf("default tunnel document not shown:\n%s", got)
	}
	c.DocumentName = "Example-TunnelDocument"
	if got := strings.Join(f.m.connectionDetails(c, p, true), "\n"); !strings.Contains(got, "Example-TunnelDocument") {
		t.Fatalf("tunnel document not shown:\n%s", got)
	}
}

func TestImportPreviewShowsTunnelDocument(t *testing.T) {
	f := setupUI(t)
	local := domain.Connection{ID: "same", Kind: "db", Name: "existing", AccountID: "111111111111", Region: "us-east-1", DocumentName: "Example-TunnelDocument"}
	imported := local
	imported.DocumentName = ""
	f.conns.plan = domain.ImportPlan{
		Additions: []domain.Connection{{Kind: "db", Name: "added", AccountID: "111111111111", Region: "eu-west-1", DocumentName: "Example-AddedDocument"}},
		Conflicts: []domain.Connection{imported},
		Existing:  []domain.Connection{local},
	}
	key(f.m, "6")
	key(f.m, "i")
	key(f.m, "enter")
	lines := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{
		"+ added | 111111111111 | eu-west-1 | document Example-AddedDocument",
		"Conflict: existing | 111111111111 | us-east-1 | document Example-TunnelDocument -> AWS-StartPortForwardingSessionToRemoteHost (default)",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("missing %q:\n%s", want, lines)
		}
	}
}

func TestDatabaseFormsHaveDestinationField(t *testing.T) {
	for _, engine := range []string{engPostgres, engDocumentDB} {
		f := setupUI(t)
		f.m.screen = databases
		f.m.newDatabaseForm(engine)
		fields := f.m.form.fields
		last, before := fields[len(fields)-1], fields[len(fields)-2]
		if before.id != "document" || last.id != "destination" || last.label != "Destination (request / fixed)" || last.value != "request" {
			t.Fatalf("%s form: last fields %+v %+v", engine, before, last)
		}
	}
}

func TestEditFixedDestination(t *testing.T) {
	f := setupUI(t)
	manager, err := connections.New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	c := domain.Connection{ID: "saved", Kind: "db", Name: "db", AccountID: "111111111111", Partition: "aws", Region: "us-east-1", Endpoint: "db.example.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1", DBAccountID: "111111111111", DocumentName: "Example-FixedDocument"}
	if err = manager.Save(c); err != nil {
		t.Fatal(err)
	}
	f.m.services.Connections = manager
	f.m.screen = databases
	finish(f.m, f.m.load())
	saved := func() domain.Connection {
		got, _ := manager.List()
		if len(got) != 1 {
			t.Fatalf("bookmarks: %+v", got)
		}
		return got[0]
	}

	key(f.m, "e")
	if f.m.form.get("destination") != "request" {
		t.Fatalf("destination = %q", f.m.form.get("destination"))
	}
	f.m.form.set("destination", "fixed")
	key(f.m, "ctrl+s")
	if !saved().FixedDestination {
		t.Fatalf("fixed not saved: %s", f.m.status)
	}

	key(f.m, "e")
	if f.m.form.get("destination") != "fixed" {
		t.Fatalf("re-opened destination = %q", f.m.form.get("destination"))
	}
	f.m.form.set("destination", "request")
	key(f.m, "ctrl+s")
	if saved().FixedDestination {
		t.Fatalf("request did not clear the flag: %s", f.m.status)
	}

	key(f.m, "e")
	f.m.form.set("destination", " Fixed ")
	key(f.m, "ctrl+s")
	if !saved().FixedDestination {
		t.Fatalf("' Fixed ' not saved: %s", f.m.status)
	}

	key(f.m, "e")
	f.m.form.set("destination", "yes")
	key(f.m, "ctrl+s")
	if f.m.form == nil || f.m.status != "Choose request or fixed for the destination." {
		t.Fatalf("yes accepted: form %v status %q", f.m.form, f.m.status)
	}

	f.m.form.set("destination", "fixed")
	f.m.form.set("document", "")
	key(f.m, "ctrl+s")
	if f.m.form == nil || !strings.Contains(f.m.status, "a fixed destination needs a tunnel document") {
		t.Fatalf("cleared document accepted: form %v status %q", f.m.form, f.m.status)
	}
	if got := saved(); !got.FixedDestination || got.DocumentName != "Example-FixedDocument" {
		t.Fatalf("refused edit changed the bookmark: %+v", got)
	}
}

func TestUnverifiedDestinationNote(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	c := domain.Connection{Kind: "db", Name: "db", Partition: "aws", AccountID: p.AccountID, Region: "us-east-1", Endpoint: "db.example.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1", DocumentName: "Example-FixedDocument", FixedDestination: true}
	const note = "Destination fixed by Example-FixedDocument; easy-sso cannot confirm it is this endpoint. Your client's TLS check can."
	got := strings.Join(f.m.connectionDetails(c, p, true), "\n")
	if !strings.Contains(got, note) || !strings.Contains(got, "Example-FixedDocument (fixed destination)") {
		t.Fatalf("note or label missing:\n%s", got)
	}
	if unverifiedDestination(c) != note {
		t.Fatalf("unverifiedDestination = %q", unverifiedDestination(c))
	}
	for name, mutate := range map[string]func(*domain.Connection){
		"postgres-iam": func(c *domain.Connection) { c.AuthMode, c.Database, c.DBUser = "postgres-iam", "app", "app_user" },
		"docdb-iam":    func(c *domain.Connection) { c.AuthMode = "docdb-iam" },
		"no flag":      func(c *domain.Connection) { c.FixedDestination = false },
	} {
		d := c
		mutate(&d)
		if got := strings.Join(f.m.connectionDetails(d, p, true), "\n"); strings.Contains(got, "cannot confirm it is this endpoint") {
			t.Fatalf("%s: note shown:\n%s", name, got)
		}
		if unverifiedDestination(d) != "" {
			t.Fatalf("%s: unverifiedDestination = %q", name, unverifiedDestination(d))
		}
	}
}

func TestImportPreviewShowsFixedDestination(t *testing.T) {
	f := setupUI(t)
	local := domain.Connection{ID: "same", Kind: "db", Name: "existing", AccountID: "111111111111", Region: "us-east-1", DocumentName: "Example-FixedDocument"}
	imported := local
	imported.FixedDestination = true
	f.conns.plan = domain.ImportPlan{
		Additions: []domain.Connection{{Kind: "db", Name: "added", AccountID: "111111111111", Region: "eu-west-1", DocumentName: "Example-FixedDocument", FixedDestination: true}},
		Conflicts: []domain.Connection{imported},
		Existing:  []domain.Connection{local},
	}
	key(f.m, "6")
	key(f.m, "i")
	key(f.m, "enter")
	lines := strings.Join(f.m.reviewContent(), "\n")
	for _, want := range []string{
		"+ added | 111111111111 | eu-west-1 | document Example-FixedDocument (fixed destination)",
		"document Example-FixedDocument -> Example-FixedDocument (fixed destination)",
	} {
		if !strings.Contains(lines, want) {
			t.Fatalf("missing %q:\n%s", want, lines)
		}
	}
}

func TestNewFixedDestinationNeedsTunnelDocument(t *testing.T) {
	const want = "a fixed destination needs a tunnel document"
	// A new connection never goes through Validate before its review, so the
	// form itself must refuse a fixed destination without a document.
	cases := map[string]struct {
		engine string
		host   string
		values []string
	}{
		engPostgres:   {engPostgres, "db.example.com", []string{"us-east-1", "5432", "i-0123456789abcdef0", "us-east-1", "15432", "tunnel", "", "", "", "", "fixed"}},
		engDocumentDB: {engDocumentDB, docdbHost, []string{"us-east-1", "27017", "i-0123456789abcdef0", "us-east-1", "37017", "tunnel", "", "", "", "fixed"}},
	}
	for name, tc := range cases {
		f := setupUI(t)
		newDatabaseForm(t, f, tc.engine, tc.host)
		key(f.m, "enter")
		fill(f.m, tc.values...)
		if f.m.form == nil || f.m.review != nil || f.m.status != want {
			t.Fatalf("%s: form %v review %v status %q", name, f.m.form, f.m.review, f.m.status)
		}
	}
}

func TestUnverifiedDestinationNeedsDocument(t *testing.T) {
	c := domain.Connection{Kind: "db", FixedDestination: true}
	if got := unverifiedDestination(c); got != "" {
		t.Fatalf("note without a document: %q", got)
	}
}

func TestRunDetailsShowUnverifiedDestination(t *testing.T) {
	f := setupUI(t)
	c := domain.Connection{Kind: "db", Name: "db", Partition: "aws", AccountID: "111111111111", Region: "us-east-1", Endpoint: "db.example.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1", DocumentName: "Example-FixedDocument", FixedDestination: true}
	const note = "Destination fixed by Example-FixedDocument; easy-sso cannot confirm it is this endpoint. Your client's TLS check can."
	if got := strings.Join(f.m.runDetails(domain.Run{Connection: c}), "\n"); !strings.Contains(got, note) {
		t.Fatalf("fixed tunnel-only run lacks the note:\n%s", got)
	}
	c.AuthMode, c.Database, c.DBUser = "postgres-iam", "app", "app_user"
	if got := strings.Join(f.m.runDetails(domain.Run{Connection: c}), "\n"); strings.Contains(got, "cannot confirm it is this endpoint") {
		t.Fatalf("IAM run shows the note:\n%s", got)
	}
}
