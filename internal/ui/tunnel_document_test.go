package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/connections"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

func TestDatabaseFormsHaveTunnelDocumentField(t *testing.T) {
	for _, engine := range []string{engPostgres, engDocumentDB} {
		f := setupUI(t)
		f.m.screen = databases
		f.m.newDatabaseForm(engine)
		last := f.m.form.fields[len(f.m.form.fields)-1]
		if last.id != "document" || last.label != "Tunnel document (optional)" || last.value != "" {
			t.Fatalf("%s form: last field %+v", engine, last)
		}
	}
}

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
