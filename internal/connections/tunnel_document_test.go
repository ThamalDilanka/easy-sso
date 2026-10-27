package connections

import (
	"strings"
	"testing"
)

const exampleTunnelARN = "arn:aws:ssm:ap-south-1:111122223333:document/Example-TunnelDocument"

func TestParseTunnelDocument(t *testing.T) {
	d, err := ParseTunnelDocument("Example-TunnelDocument")
	if err != nil || d.ARN || d.Name != "Example-TunnelDocument" {
		t.Fatalf("plain name: %+v %v", d, err)
	}
	d, err = ParseTunnelDocument(exampleTunnelARN)
	if err != nil || !d.ARN || d.Name != "Example-TunnelDocument" || d.Region != "ap-south-1" {
		t.Fatalf("ARN: %+v %v", d, err)
	}
	for _, bad := range []string{"", "ab", "Example Tunnel", "arn:aws:ssm:ap-south-1:1111:document/Example-TunnelDocument"} {
		if _, err = ParseTunnelDocument(bad); err == nil {
			t.Fatalf("accepted malformed document %q", bad)
		}
	}
	for _, blocked := range []string{
		"SSM-SessionManagerRunShell",
		"AWS-StartPortForwardingSession",
		"AWS-StartSSHSession",
		"AWS-StartNonInteractiveCommand",
		"AWS-StartInteractiveCommand",
		"aws-startsshsession",
		"arn:aws:ssm:ap-south-1:111122223333:document/SSM-SessionManagerRunShell",
	} {
		_, err = ParseTunnelDocument(blocked)
		if err == nil || !strings.Contains(err.Error(), "not a remote-host port forwarding document") {
			t.Fatalf("accepted non-tunnel document %q: %v", blocked, err)
		}
	}
	_, err = ParseTunnelDocument("aws-startportforwardingsessiontoremotehost")
	if err == nil || !strings.Contains(err.Error(), "is the default") {
		t.Fatalf("explicit default document: %v", err)
	}
}

func TestValidateTunnelDocument(t *testing.T) {
	for _, doc := range []string{"", "Example-TunnelDocument", exampleTunnelARN} {
		c := db()
		c.DocumentName = doc
		if err := Validate(c); err != nil {
			t.Fatalf("rejected %q: %v", doc, err)
		}
	}
	// The tunnel starts in the relay region, not the database region.
	for doc, want := range map[string]string{
		"arn:aws:ssm:eu-west-1:111122223333:document/Example-TunnelDocument":     "partition and relay region",
		"arn:aws-cn:ssm:cn-north-1:111122223333:document/Example-TunnelDocument": "partition and relay region",
		"Example-TunnelDocument\n":   "control characters",
		"SSM-SessionManagerRunShell": "not a remote-host port forwarding document",
	} {
		c := db()
		c.DocumentName = doc
		if err := Validate(c); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: want %q, got %v", doc, want, err)
		}
	}
}

func TestResourceKeyTunnelDocument(t *testing.T) {
	plain, custom := db(), db()
	custom.DocumentName = "Example-TunnelDocument"
	if got := ResourceKey(plain); got != "db|aws|111111111111|db.example.rds.amazonaws.com|5432|eu-west-1|i-0123456789abcdef0|ap-south-1" {
		t.Fatalf("existing database key changed: %s", got)
	}
	if ResourceKey(plain) == ResourceKey(custom) {
		t.Fatal("a different tunnel document must be a separate bookmark")
	}
}

func TestSchemaAcceptsTunnelDocument(t *testing.T) {
	c := db()
	c.DocumentName = exampleTunnelARN
	if _, err := decode(jsonDoc(t, c)); err != nil {
		t.Fatalf("database record with a tunnel document rejected: %v", err)
	}
}
