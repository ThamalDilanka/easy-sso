package connections

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

const exampleDocumentARN = "arn:aws:ssm:us-east-1:111122223333:document/Example-ShellDocument"

func shellBookmark(document string) domain.Connection {
	return domain.Connection{ID: "shell-1", Kind: "ec2", Name: "shell", Partition: "aws", AccountID: "111122223333", Region: "us-east-1", InstanceID: "i-0123456789abcdef0", DocumentName: document}
}

func TestParseSessionDocument(t *testing.T) {
	d, err := ParseSessionDocument("Example-ShellDocument")
	if err != nil || d.ARN || d.Name != "Example-ShellDocument" {
		t.Fatalf("plain name: %+v %v", d, err)
	}
	d, err = ParseSessionDocument(exampleDocumentARN)
	if err != nil || !d.ARN || d.Name != "Example-ShellDocument" || d.Partition != "aws" || d.Region != "us-east-1" || d.AccountID != "111122223333" {
		t.Fatalf("ARN: %+v %v", d, err)
	}
	for _, bad := range []string{
		"",
		"ab",
		strings.Repeat("a", 129),
		"Example Shell",
		"Example/Doc",
		"arn:aws:ssm:us-east-1:1111:document/Example-ShellDocument",
		"arn:aws:s3:us-east-1:111122223333:document/X1y",
		"arn:aws:ssm:us-east-1:111122223333:document/Example-ShellDocument/extra",
	} {
		if _, err = ParseSessionDocument(bad); err == nil {
			t.Fatalf("accepted malformed document %q", bad)
		}
	}
	for _, blocked := range []string{
		"AWS-StartPortForwardingSession",
		"AWS-StartPortForwardingSessionToRemoteHost",
		"AWS-StartSSHSession",
		"AWS-StartNonInteractiveCommand",
		"aws-startsshsession",
		"arn:aws:ssm:us-east-1:111122223333:document/AWS-StartSSHSession",
	} {
		_, err = ParseSessionDocument(blocked)
		if err == nil || !strings.Contains(err.Error(), "not a shell session document") {
			t.Fatalf("accepted non-shell document %q: %v", blocked, err)
		}
	}
}

func TestValidateSessionDocument(t *testing.T) {
	for _, doc := range []string{"", "Example-ShellDocument", exampleDocumentARN} {
		if err := Validate(shellBookmark(doc)); err != nil {
			t.Fatalf("rejected %q: %v", doc, err)
		}
	}
	for doc, want := range map[string]string{
		"arn:aws:ssm:eu-west-1:111122223333:document/Example-ShellDocument":     "partition and region",
		"arn:aws-cn:ssm:cn-north-1:111122223333:document/Example-ShellDocument": "partition and region",
		"Example-ShellDocument\n": "control characters",
		"AWS-StartSSHSession":     "not a shell session document",
	} {
		err := Validate(shellBookmark(doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: want %q, got %v", doc, want, err)
		}
	}
}

func TestResourceKeySessionDocument(t *testing.T) {
	plain, custom := shellBookmark(""), shellBookmark("Example-ShellDocument")
	if got := ResourceKey(plain); got != "ec2|aws|111122223333|us-east-1|i-0123456789abcdef0" {
		t.Fatalf("existing EC2 key changed: %s", got)
	}
	if ResourceKey(plain) == ResourceKey(custom) {
		t.Fatal("a different session document must be a separate bookmark")
	}
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	plain.ID, custom.ID = "", ""
	for _, c := range []domain.Connection{plain, custom} {
		if err = m.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	saved, _ := m.List()
	if len(saved) != 2 {
		t.Fatalf("want 2 bookmarks, got %d", len(saved))
	}
	first := saved[0]
	first.DocumentName = saved[1].DocumentName
	if err = m.Update(first); err == nil || !strings.Contains(err.Error(), "another bookmark already uses this destination") {
		t.Fatalf("edit collided with another bookmark: %v", err)
	}
}

func TestSchemaAcceptsLegacyAndDocumentFiles(t *testing.T) {
	legacy := []byte(`{"schemaVersion":1,"connections":[{"id":"a","kind":"ec2","name":"n","partition":"aws","accountId":"111122223333","region":"us-east-1","instanceId":"i-0123456789abcdef0"}]}`)
	if _, err := decode(legacy); err != nil {
		t.Fatalf("legacy file rejected: %v", err)
	}
	if _, err := decode(jsonDoc(t, shellBookmark(exampleDocumentARN))); err != nil {
		t.Fatalf("document file rejected: %v", err)
	}
}

func TestExportImportKeepsSessionDocument(t *testing.T) {
	m, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Save(shellBookmark("Example-ShellDocument")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.json")
	if err = m.Export(path); err != nil {
		t.Fatal(err)
	}
	other, err := New(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := other.Preview(path)
	if err != nil || len(plan.Additions) != 1 || plan.Additions[0].DocumentName != "Example-ShellDocument" {
		t.Fatalf("import lost the session document: %+v %v", plan, err)
	}
}
