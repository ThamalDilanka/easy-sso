package sessions

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestArgumentsTunnelDocument(t *testing.T) {
	tunnel, _, _, _ := fixture(t)
	tunnel.DocumentName = "Example-TunnelDocument"
	got := strings.Join(arguments(domain.Run{ID: runID(), Connection: tunnel}), " ")
	if strings.Count(got, "--document-name") != 1 || !strings.Contains(got, "--document-name Example-TunnelDocument --parameters") {
		t.Fatalf("tunnel document not passed: %s", got)
	}
}

func TestTunnelAccessDeniedNamesDocument(t *testing.T) {
	for document, want := range map[string]string{"Example-TunnelDocument": "the Example-TunnelDocument document", "": "the AWS-StartPortForwardingSessionToRemoteHost document"} {
		d := &startupDiagnostic{document: document}
		_, _ = d.Write([]byte("An error occurred (AccessDeniedException) when calling StartSession"))
		if got := d.tunnelFailure(); !strings.Contains(got, want) {
			t.Fatalf("document %q: %s", document, got)
		}
	}
}

func TestDeniedTunnelNamesItsDocument(t *testing.T) {
	c, role, snapshot, cli := fixture(t)
	c.DocumentName = "Example-TunnelDocument"
	probe := func(int, domain.Child) (bool, error) { return true, nil }
	m, err := NewWithOptions(deniedCLI{cli}, t.TempDir(), "/fake/easysso", &bookmarks{}, Options{Attempts: 3, Interval: time.Millisecond, Probe: probe})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	_, _ = m.Open(context.Background(), c, role, snapshot)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runs := m.List(); len(runs) == 1 && strings.Contains(runs[0].Error, "Access denied") {
			if !strings.Contains(runs[0].Error, "the Example-TunnelDocument document") {
				t.Fatalf("denied tunnel did not name its document: %s", runs[0].Error)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tunnel never reported access denied: %+v", m.List())
}

func TestArgumentsFixedDestination(t *testing.T) {
	tunnel, _, _, _ := fixture(t)
	tunnel.DocumentName = "Example-FixedDocument"
	tunnel.FixedDestination = true
	tunnel.LocalPort = 15432
	got := arguments(domain.Run{ID: runID(), Connection: tunnel})
	joined := strings.Join(got, " ")
	want := `--document-name Example-FixedDocument --parameters {"localPortNumber":["15432"]}`
	if !strings.HasSuffix(joined, want) {
		t.Fatalf("fixed tunnel argv: %s", joined)
	}
	if strings.Contains(joined, "host") || strings.Contains(joined, `"portNumber"`) {
		t.Fatalf("fixed tunnel sent destination parameters: %s", joined)
	}
}

func TestTunnelParameterMismatchMessages(t *testing.T) {
	const hostText = `An error occurred (InvalidParameters) when calling the StartSession operation: Parameter "host" is not defined in the document.`
	notDefined := "The Example-FixedDocument document fixes the destination. Edit the bookmark (e), set Destination to fixed, and save. Database login was not attempted."
	needsHost := "The Example-FixedDocument document needs the host and port. Edit the bookmark (e), set Destination to request, and save. Database login was not attempted."
	cases := []struct {
		name   string
		fixed  bool
		writes []string
		want   string
	}{
		{"host not defined", false, []string{hostText}, notDefined},
		{"fixed needs host", true, []string{"An error occurred (InvalidParameters) when calling the StartSession operation: missing"}, needsHost},
		{"unrelated error", false, []string{"An error occurred (TargetNotConnected) when calling the StartSession operation"}, ""},
		{"fixed unrelated error", true, []string{"An error occurred (TargetNotConnected) when calling the StartSession operation"}, ""},
		{"fixed ignores host text without code", true, []string{`Parameter "host" is not defined`}, ""},
		{"default document never reports mismatch", false, []string{hostText}, ""},
		{"denied wins", false, []string{"An error occurred (AccessDeniedException) when calling StartSession; " + hostText}, "Access denied"},
		{"split across writes", false, []string{`An error occurred (InvalidParameters) when calling the StartSession operation: Parameter "host" is`, ` not defined in the document.`}, notDefined},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &startupDiagnostic{document: "Example-FixedDocument", fixed: c.fixed}
			if c.name == "default document never reports mismatch" {
				d.document = ""
			}
			for _, w := range c.writes {
				_, _ = d.Write([]byte(w))
			}
			got := d.tunnelFailure()
			if c.want == "Access denied" {
				if !strings.HasPrefix(got, "Access denied") {
					t.Fatalf("got %q", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestStartupDiagnosticKeepsBoundedTail(t *testing.T) {
	d := &startupDiagnostic{}
	_, _ = d.Write([]byte(strings.Repeat("x", 1000)))
	if len(d.tail) != 128 {
		t.Fatalf("tail length %d", len(d.tail))
	}
}

func TestSameBindingComparesFixedDestination(t *testing.T) {
	a := domain.Run{ID: runID(), Connection: domain.Connection{Kind: "db", InstanceID: "i-0123456789abcdef0", DocumentName: "Example-FixedDocument", FixedDestination: true}}
	b := a
	if !sameBinding(a, b) {
		t.Fatal("identical runs differ")
	}
	b.Connection.FixedDestination = false
	if sameBinding(a, b) {
		t.Fatal("a recovered run with another destination mode was treated as the same binding")
	}
}
