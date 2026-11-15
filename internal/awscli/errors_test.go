package awscli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ThamalDilanka/easy-sso/internal/domain"
)

// Fake material that must never reach an error message.
const leakedStderr = " AKIAEXAMPLE aws_access_key_id=AKIAEXAMPLE aws_session_token=TESTTOKEN code=ABCD-EFGH https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH"

func TestClassifyFailure(t *testing.T) {
	for _, tc := range []struct {
		name, stderr string
		want         error
	}{
		{"not assigned", "\nAn error occurred (ForbiddenException) when calling the GetRoleCredentials operation: No access", ErrAccessDenied},
		{"access denied", "An error occurred (AccessDeniedException) when calling the DescribeSessions operation: User is not authorized", ErrAccessDenied},
		{"sso token expired", "Error when retrieving token from sso: Token has expired and refresh failed", ErrSSOSignIn},
		{"sso session expired", "The SSO session associated with this profile has expired or is otherwise invalid. To refresh this SSO session run aws sso login with the corresponding profile.", ErrSSOSignIn},
		{"sso unauthorized", "An error occurred (UnauthorizedException) when calling the GetRoleCredentials operation: Session token not found or invalid", ErrSSOSignIn},
		{"sso token missing", "aws: [ERROR]: Error loading SSO Token: Token for root does not exist", ErrSSOSignIn},
		{"device code expired", "The pending authorization to retrieve an SSO token has expired. The device authorization flow to retrieve an SSO token must be restarted.", ErrSSOSignIn},
		{"role credentials expired", "An error occurred (ExpiredTokenException) when calling the DescribeSessions operation: The security token included in the request is expired", ErrRoleCredsExpired},
		{"sts token expired", "An error occurred (ExpiredToken) when calling the GetCallerIdentity operation: The security token included in the request is expired", ErrRoleCredsExpired},
		{"skew in profile name", "The config profile (askew-dev) could not be found", ErrRequestFailed},
		{"skew in session name", "An error occurred (ForbiddenException) when calling the GetRoleCredentials operation: No access for askew", ErrAccessDenied},
		{"denied in name", "The config profile (accessdenied-test) could not be found", ErrRequestFailed},
		{"signature expired", "An error occurred (SignatureDoesNotMatch) when calling the GetCallerIdentity operation: Signature expired: 20260101T000000Z is now earlier than 20260101T001000Z", ErrClockSkew},
		{"signature not yet current", "An error occurred (SignatureDoesNotMatch) when calling the GetCallerIdentity operation: Signature not yet current: 20260101T010000Z is still later than 20260101T001500Z", ErrClockSkew},
		{"request expired", "An error occurred (RequestExpired) when calling the DescribeSessions operation: Request has expired.", ErrClockSkew},
		{"time too skewed", "An error occurred (RequestTimeTooSkewed) when calling the PutObject operation: The difference between the request time and the current time is too large.", ErrClockSkew},
		{"bare signature mismatch", "An error occurred (SignatureDoesNotMatch) when calling the GetCallerIdentity operation: The request signature we calculated does not match the signature you provided.", ErrRequestFailed},
		{"endpoint unreachable", `Could not connect to the endpoint URL: "https://sts.us-east-1.amazonaws.com/"`, ErrNetwork},
		{"connect timeout", `aws: [ERROR]: Connect timeout on endpoint URL: "https://sts.us-east-1.amazonaws.com/"`, ErrNetwork},
		{"proxy", `aws: [ERROR]: Failed to connect to proxy URL: "http://proxy.example.invalid:3128"`, ErrNetwork},
		{"unknown", "aws: [ERROR]: something unexpected", ErrRequestFailed},
		{"empty", "", ErrRequestFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyFailure([]byte(tc.stderr+leakedStderr), false)
			if !errors.Is(got, tc.want) {
				t.Fatalf("classified as %q, want %q", got, tc.want)
			}
			for _, secret := range []string{"AKIAEXAMPLE", "aws_session_token", "TESTTOKEN", "ABCD-EFGH", "user_code", "amazonaws.com", "example.invalid", "20260101"} {
				if strings.Contains(got.Error(), secret) {
					t.Fatalf("message %q echoes CLI output %q", got, secret)
				}
			}
		})
	}
}

func TestClassifyLoginFailure(t *testing.T) {
	for _, stderr := range []string{
		"An error occurred (AccessDeniedException) when calling the CreateToken operation: ",
		"An error occurred (ExpiredTokenException) when calling the CreateToken operation: ",
	} {
		if got := classifyFailure([]byte(stderr+leakedStderr), true); got != ErrSSOSignIn {
			t.Fatalf("sign-in output %q classified as %q", stderr, got)
		}
	}
	if got := classifyFailure([]byte("Connect timeout on endpoint URL: \"https://oidc.us-east-1.amazonaws.com/\""), true); got != ErrNetwork {
		t.Fatalf("sign-in network failure classified as %q", got)
	}
}

func TestCLIFailureMessagesAreSafe(t *testing.T) {
	for _, e := range []*SafeError{ErrAccessDenied, ErrSSOSignIn, ErrRoleCredsExpired, ErrClockSkew, ErrNetwork, ErrRequestFailed} {
		var safe *SafeError
		if !errors.As(fmt.Errorf("wrapped: %w", e), &safe) || safe.Error() == "" {
			t.Fatalf("%q is not a safe AWS CLI error", e)
		}
	}
	if !strings.Contains(ErrClockSkew.Error(), "wsl --shutdown") {
		t.Fatal("clock skew advice lacks the WSL clock resync")
	}
	if ErrRequestFailed.Error() != "AWS CLI request failed; check permissions, network, CLI configuration and SSO sign-in" {
		t.Fatal("generic CLI failure text changed")
	}
}

func TestRunClassifiesStderrWithoutEchoingIt(t *testing.T) {
	c := fakeClient(t, "printf '%s\\n' 'An error occurred (ForbiddenException) when calling the GetRoleCredentials operation: No access"+leakedStderr+"' >&2\nprintf '%s' 'SECRET'\nexit 255\n")
	_, err := c.Export(context.Background(), domain.Profile{Name: "selected"})
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatal(err)
	}
	for _, secret := range []string{"AKIAEXAMPLE", "TESTTOKEN", "SECRET", "ABCD-EFGH"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("CLI output leaked: %v", err)
		}
	}
	c = fakeClient(t, "printf '%s\\n' 'aws: [ERROR]: Connect timeout on endpoint URL: \"https://sts.us-east-1.amazonaws.com/\"' >&2\nexit 255\n")
	if _, err = c.Identity(context.Background(), snapshot(), "us-east-1"); !errors.Is(err, ErrNetwork) {
		t.Fatal(err)
	}
}

func TestSnapshotValidationErrorsAreSafe(t *testing.T) {
	expired := snapshot()
	expired.Expiration = expired.Expiration.AddDate(0, 0, -1)
	if err := ValidateSnapshot(expired); !errors.Is(err, ErrCredentialsExpired) {
		t.Fatal(err)
	}
	var safe *SafeError
	if err := ValidateSnapshot(domain.Snapshot{}); !errors.As(err, &safe) {
		t.Fatal(err)
	}
}
