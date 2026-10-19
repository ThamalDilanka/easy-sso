package awscli

import (
	"context"
	"testing"
)

func TestStartSessionDocumentAllowlist(t *testing.T) {
	c := fakeClient(t, "exit 0\n")
	base := []string{"ssm", "start-session", "--region", "us-east-1", "--target", "i-0123456789abcdef0", "--reason", "easy-sso:run"}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
	parameters := `{"host":["db.example.com"],"portNumber":["5432"],"localPortNumber":["15432"]}`
	for _, args := range [][]string{
		base,
		with("--document-name", "Example-ShellDocument"),
		with("--document-name", "arn:aws:ssm:us-east-1:111122223333:document/Example-ShellDocument"),
		with("--document-name", "AWS-StartPortForwardingSessionToRemoteHost", "--parameters", parameters),
	} {
		child, err := c.Start(context.Background(), snapshot(), args, nil, nil, nil)
		if err != nil {
			t.Fatalf("refused %v: %v", args, err)
		}
		_ = child.Wait()
	}
	for _, args := range [][]string{
		with("--document-name", "Example-ShellDocument", "--parameters", parameters),
		with("--document-name", "AWS-StartSSHSession"),
		with("--document-name", "aws-startportforwardingsession"),
		with("--document-name", "arn:aws:ssm:eu-west-1:111122223333:document/Example-ShellDocument"),
		with("--document-name", "Example Shell"),
		with("--document-name", "Example-ShellDocument", "--document-name", "Example-ShellDocument"),
		with("--profile", "other"),
		with("--debug", "x"),
		{"ssm", "start-session", "--target", "i-0123456789abcdef0", "--document-name", "Example-ShellDocument"},
		{"ssm", "start-session", "--region", "us-east-1", "--target", "db.example.com", "--document-name", "Example-ShellDocument"},
		{"ssm", "send-command", "--region", "us-east-1", "--target", "i-0123456789abcdef0"},
	} {
		if _, err := c.Start(context.Background(), snapshot(), args, nil, nil, nil); err == nil {
			t.Fatalf("accepted unsafe arguments %v", args)
		}
	}
}
