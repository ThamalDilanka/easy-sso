package sessions

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

func TestArgumentsSessionDocument(t *testing.T) {
	id := runID()
	shell := domain.Connection{Kind: "ec2", Name: "shell", Partition: "aws", AccountID: "111122223333", Region: "us-east-1", InstanceID: "i-0123456789abcdef0"}
	want := []string{"ssm", "start-session", "--target", "i-0123456789abcdef0", "--region", "us-east-1", "--reason", "easy-sso:" + id}
	if got := arguments(domain.Run{ID: id, Connection: shell}); !reflect.DeepEqual(got, want) {
		t.Fatalf("default shell argv changed: %v", got)
	}
	shell.DocumentName = "Example-ShellDocument"
	want = append(want, "--document-name", "Example-ShellDocument")
	if got := arguments(domain.Run{ID: id, Connection: shell}); !reflect.DeepEqual(got, want) {
		t.Fatalf("session document not passed: %v", got)
	}
	tunnel, _, _, _ := fixture(t)
	got := strings.Join(arguments(domain.Run{ID: id, Connection: tunnel}), " ")
	if strings.Count(got, "--document-name") != 1 || !strings.Contains(got, "--document-name AWS-StartPortForwardingSessionToRemoteHost --parameters") {
		t.Fatalf("tunnel argv changed: %s", got)
	}
}

func TestSameBindingComparesSessionDocument(t *testing.T) {
	a := domain.Run{ID: runID(), Connection: domain.Connection{Kind: "ec2", InstanceID: "i-0123456789abcdef0", DocumentName: "Example-ShellDocument"}}
	b := a
	if !sameBinding(a, b) {
		t.Fatal("identical runs differ")
	}
	b.Connection.DocumentName = ""
	if sameBinding(a, b) {
		t.Fatal("a recovered run with another session document was treated as the same binding")
	}
}

func TestAccessDeniedNamesSessionDocument(t *testing.T) {
	for document, want := range map[string]string{"Example-ShellDocument": "(Example-ShellDocument)", "": "(SSM-SessionManagerRunShell)"} {
		d := &startupDiagnostic{document: document}
		_, _ = d.Write([]byte("An error occurred (AccessDeniedException) when calling StartSession"))
		if got := d.failure(); !strings.Contains(got, want) {
			t.Fatalf("document %q: %s", document, got)
		}
	}
}
