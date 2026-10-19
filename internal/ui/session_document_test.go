package ui

import (
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

func TestNewEC2FormHasSessionDocumentField(t *testing.T) {
	f := setupUI(t)
	f.m.screen = ec2
	f.m.newForm()
	if len(f.m.form.fields) != 4 || f.m.form.fields[3].label != "Session document (optional)" || f.m.form.fields[3].value != "" {
		t.Fatalf("EC2 form fields: %+v", f.m.form.fields)
	}
	for i, v := range []string{"shell", "i-0123456789abcdef0", "us-east-1"} {
		f.m.form.fields[i].value = v
	}
	f.m.form.index = 3
	key(f.m, "enter")
	if f.m.review == nil || f.m.review.connection.DocumentName != "" || f.m.review.connection.InstanceID != "i-0123456789abcdef0" {
		t.Fatalf("blank document did not reach review as default: %+v", f.m.review)
	}
}

func TestReviewShowsSessionDocument(t *testing.T) {
	f := setupUI(t)
	p := f.m.services.Profiles[0]
	c := domain.Connection{Kind: "ec2", Name: "shell", Partition: "aws", AccountID: p.AccountID, Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}
	if got := strings.Join(f.m.connectionDetails(c, p, true), "\n"); !strings.Contains(got, "Default (Session Manager preferences)") {
		t.Fatalf("default document not shown:\n%s", got)
	}
	c.DocumentName = "Example-ShellDocument"
	if got := strings.Join(f.m.connectionDetails(c, p, true), "\n"); !strings.Contains(got, "Document") || !strings.Contains(got, "Example-ShellDocument") {
		t.Fatalf("session document not shown:\n%s", got)
	}
	tunnel := domain.Connection{Kind: "db", Name: "db", Partition: "aws", AccountID: p.AccountID, Region: "us-east-1", Endpoint: "db.example.com", DBPort: 5432, LocalPort: 15432, RelayInstanceID: "i-0123456789abcdef0", RelayRegion: "us-east-1"}
	if got := strings.Join(f.m.connectionDetails(tunnel, p, true), "\n"); strings.Contains(got, "Session Manager preferences") {
		t.Fatalf("database review shows a session document:\n%s", got)
	}
}

func TestImportPreviewShowsSessionDocument(t *testing.T) {
	f := setupUI(t)
	f.conns.plan = domain.ImportPlan{
		Additions: []domain.Connection{{Kind: "ec2", Name: "imported", AccountID: "111111111111", Region: "eu-west-1", DocumentName: "Example-AddedDocument"}},
		Conflicts: []domain.Connection{{Kind: "ec2", Name: "existing", AccountID: "111111111111", Region: "us-east-1", DocumentName: "Example-ShellDocument"}},
	}
	key(f.m, "6")
	key(f.m, "i")
	key(f.m, "enter")
	view := f.m.View()
	for _, want := range []string{"+ imported | 111111111111 | eu-west-1 | document Example-AddedDocument", "Conflict: existing | 111111111111 | us-east-1 | document Example-ShellDocument"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
}

func TestOpenShellWithAnotherDocumentIsNotReused(t *testing.T) {
	f := setupUI(t)
	activateOne(t, f)
	p, _ := f.m.profile()
	plain := domain.Connection{ID: "plain", Kind: "ec2", Name: "plain", Partition: "aws", AccountID: p.AccountID, Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}
	custom := plain
	custom.ID, custom.Name, custom.DocumentName = "custom", "custom", "Example-ShellDocument"
	f.m.runs = []domain.Run{{ID: "run-plain", Connection: plain, Profile: p, State: "Connected"}}
	f.m.connectionReview(custom)
	if f.m.review.duplicate {
		t.Fatal("review offered a shell that runs another session document")
	}
	f.m.review = nil
	f.m.connections = []domain.Connection{custom}
	f.m.screen, f.m.selectedRow = ec2, 0
	finish(f.m, f.m.connectSelected())
	if len(f.sessions.opened) != 1 || f.sessions.opened[0].DocumentName != "Example-ShellDocument" {
		t.Fatalf("connect focused a shell with another session document: %+v", f.sessions.opened)
	}
}

func TestImportPreviewShowsReplacedSessionDocument(t *testing.T) {
	f := setupUI(t)
	local := domain.Connection{ID: "same", Kind: "ec2", Name: "existing", AccountID: "111111111111", Region: "us-east-1", DocumentName: "Example-ShellDocument"}
	imported := local
	imported.DocumentName = ""
	f.conns.plan = domain.ImportPlan{Conflicts: []domain.Connection{imported}, Existing: []domain.Connection{local}}
	key(f.m, "6")
	key(f.m, "i")
	key(f.m, "enter")
	lines := strings.Join(f.m.reviewContent(), "\n")
	if !strings.Contains(lines, "Conflict: existing | 111111111111 | us-east-1 | document Example-ShellDocument -> Default (Session Manager preferences)") {
		t.Fatalf("replacement would silently drop the session document:\n%s", lines)
	}
}
