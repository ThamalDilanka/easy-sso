package connections

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
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

func TestValidateFixedDestination(t *testing.T) {
	fixed := db()
	fixed.DocumentName = "Example-FixedDocument"
	fixed.FixedDestination = true
	if err := Validate(fixed); err != nil {
		t.Fatalf("rejected a fixed destination with a document: %v", err)
	}
	bare := db()
	bare.FixedDestination = true
	if err := Validate(bare); err == nil || err.Error() != "a fixed destination needs a tunnel document" {
		t.Fatalf("fixed destination without a document: %v", err)
	}
	plain := fixed
	plain.FixedDestination = false
	if ResourceKey(fixed) != ResourceKey(plain) {
		t.Fatal("the fixed flag must not change the resource key")
	}
}

func TestSchemaFixedDestination(t *testing.T) {
	fixed := db()
	fixed.DocumentName = "Example-FixedDocument"
	fixed.FixedDestination = true
	if _, err := decode(jsonDoc(t, fixed)); err != nil {
		t.Fatalf("fixed database record rejected: %v", err)
	}
	// Raw records skip Validate, as an import does.
	withFlag := func(c domain.Connection) []byte {
		var doc map[string]any
		if err := json.Unmarshal(jsonDoc(t, c), &doc); err != nil {
			t.Fatal(err)
		}
		doc["connections"].([]any)[0].(map[string]any)["fixedDestination"] = true
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if _, err := decode(withFlag(db())); err == nil {
		t.Fatal("accepted a fixed destination without a tunnel document")
	}
	// A session document keeps the record otherwise valid, so only the flag can fail it.
	session := ec2()
	session.DocumentName = "Example-SessionDocument"
	if _, err := decode(jsonDoc(t, session)); err != nil {
		t.Fatalf("EC2 record with a session document rejected: %v", err)
	}
	if _, err := decode(withFlag(session)); err == nil {
		t.Fatal("accepted a fixed destination on an EC2 record")
	}
}
