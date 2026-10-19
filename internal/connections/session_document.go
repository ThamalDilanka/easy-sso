package connections

import (
	"errors"
	"regexp"
	"strings"
)

// SessionDocument is an EC2 bookmark's SSM session document, by name or by a
// full ARN for a document shared from another account.
type SessionDocument struct {
	Name, Partition, Region, AccountID string
	ARN                                bool
}

var documentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,128}$`)
var documentARNPattern = regexp.MustCompile(`^arn:(aws|aws-cn|aws-us-gov):ssm:([a-z]{2}(?:-[a-z]+)+-[0-9]+):([0-9]{12}):document/([A-Za-z0-9_.-]{3,128})$`)

// AWS-owned documents that start port forwarding, SSH or a one-shot command
// instead of an interactive shell.
var nonShellDocuments = []string{"AWS-StartPortForwardingSession", "AWS-StartPortForwardingSessionToRemoteHost", "AWS-StartSSHSession", "AWS-StartNonInteractiveCommand"}

func ParseSessionDocument(doc string) (SessionDocument, error) {
	d := SessionDocument{Name: doc}
	if m := documentARNPattern.FindStringSubmatch(doc); m != nil {
		d = SessionDocument{Name: m[4], Partition: m[1], Region: m[2], AccountID: m[3], ARN: true}
	} else if !documentNamePattern.MatchString(doc) {
		return SessionDocument{}, errors.New("session document must be a document name (3-128 letters, digits, _ . -) or a full SSM document ARN")
	}
	for _, blocked := range nonShellDocuments {
		if strings.EqualFold(d.Name, blocked) {
			return SessionDocument{}, errors.New(d.Name + " is not a shell session document; leave the field blank or use a shell document")
		}
	}
	return d, nil
}
