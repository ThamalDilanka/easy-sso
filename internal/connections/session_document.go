package connections

import (
	"errors"
	"regexp"
	"strings"
)

// SessionDocument is a bookmark's SSM document: an EC2 shell document or a
// database tunnel document, by name or by a full ARN for a document shared
// from another account.
type SessionDocument struct {
	Name, Partition, Region, AccountID string
	ARN                                bool
}

var documentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,128}$`)
var documentARNPattern = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):ssm:([a-z]{2}(?:-[a-z]+)+-[0-9]+):([0-9]{12}):document/([A-Za-z0-9_.-]{3,128})$`)

// AWS-owned documents that start port forwarding, SSH or a one-shot command
// instead of an interactive shell.
var nonShellDocuments = []string{"AWS-StartPortForwardingSession", "AWS-StartPortForwardingSessionToRemoteHost", "AWS-StartSSHSession", "AWS-StartNonInteractiveCommand"}

// DefaultTunnelDocument starts a database tunnel when a bookmark names none.
const DefaultTunnelDocument = "AWS-StartPortForwardingSessionToRemoteHost"

// AWS-owned documents that cannot forward a port to a remote host. A custom
// tunnel document must take the host, portNumber and localPortNumber
// parameters, as a copy of the default does.
var nonTunnelDocuments = []string{"SSM-SessionManagerRunShell", "AWS-StartPortForwardingSession", "AWS-StartSSHSession", "AWS-StartNonInteractiveCommand", "AWS-StartInteractiveCommand"}

func parseDocument(doc, label string) (SessionDocument, error) {
	d := SessionDocument{Name: doc}
	if m := documentARNPattern.FindStringSubmatch(doc); m != nil {
		d = SessionDocument{Name: m[4], Partition: m[1], Region: m[2], AccountID: m[3], ARN: true}
	} else if !documentNamePattern.MatchString(doc) {
		return SessionDocument{}, errors.New(label + " must be a document name (3-128 letters, digits, _ . -) or a full SSM document ARN")
	}
	return d, nil
}

func ParseSessionDocument(doc string) (SessionDocument, error) {
	d, err := parseDocument(doc, "session document")
	if err != nil {
		return d, err
	}
	for _, blocked := range nonShellDocuments {
		if strings.EqualFold(d.Name, blocked) {
			return SessionDocument{}, errors.New(d.Name + " is not a shell session document; leave the field blank or use a shell document")
		}
	}
	return d, nil
}

// ParseTunnelDocument checks a database bookmark's tunnel document.
func ParseTunnelDocument(doc string) (SessionDocument, error) {
	d, err := parseDocument(doc, "tunnel document")
	if err != nil {
		return d, err
	}
	// Naming the default would make a second bookmark for the same tunnel.
	if !d.ARN && strings.EqualFold(d.Name, DefaultTunnelDocument) {
		return SessionDocument{}, errors.New(DefaultTunnelDocument + " is the default; leave the tunnel document blank")
	}
	for _, blocked := range nonTunnelDocuments {
		if strings.EqualFold(d.Name, blocked) {
			return SessionDocument{}, errors.New(d.Name + " is not a remote-host port forwarding document; leave the field blank or use a copy of " + DefaultTunnelDocument)
		}
	}
	return d, nil
}
