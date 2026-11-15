package awscli

import (
	"regexp"
	"strings"
)

// SafeError is an AWS CLI failure whose message is fixed text written in this
// package. It never carries subprocess output, so callers may show it.
type SafeError struct{ message string }

func (e *SafeError) Error() string { return e.message }

// Failure classes for a CLI command that exited with an error. Each class
// names a likely cause; the CLI's own output is never repeated.
var (
	ErrAccessDenied     = &SafeError{"AWS denied access; check that this account and role are assigned to you in IAM Identity Center and allow this action"}
	ErrSSOSignIn        = &SafeError{"IAM Identity Center sign-in is missing, expired or was not completed; sign in again"}
	ErrRoleCredsExpired = &SafeError{"AWS rejected the role credentials as expired; activate the role again"}
	ErrClockSkew        = &SafeError{"AWS rejected the request time; sync the system clock (on WSL run wsl --shutdown from Windows) and retry"}
	ErrNetwork          = &SafeError{"AWS CLI could not reach AWS; check network, proxy and VPN settings"}
	ErrRequestFailed    = &SafeError{"AWS CLI request failed; check permissions, network, CLI configuration and SSO sign-in"}
)

// Snapshot validation failures. ValidateSnapshot reports these fixed texts.
var (
	ErrCredentialsIncomplete = &SafeError{"Activate this role before connecting: complete temporary credentials required"}
	ErrCredentialsInvalid    = &SafeError{"invalid temporary credential material"}
	ErrCredentialsExpired    = &SafeError{"Role credentials expired. Activate again to start new connections"}
)

// errorCode reads the service error code from the CLI's standard
// "An error occurred (Code) when calling ..." line.
var errorCode = regexp.MustCompile(`An error occurred \(([A-Za-z]+)\)`)

// Codes are matched exactly, so a profile name, ARN or URL in the output that
// merely contains a word such as "skew" or "denied" cannot change the class.
var codeClasses = map[string]*SafeError{
	"RequestExpired":        ErrClockSkew,
	"RequestTimeTooSkewed":  ErrClockSkew,
	"UnauthorizedException": ErrSSOSignIn,
	"ForbiddenException":    ErrAccessDenied,
	"AccessDenied":          ErrAccessDenied,
	"AccessDeniedException": ErrAccessDenied,
	"ExpiredToken":          ErrRoleCredsExpired,
	"ExpiredTokenException": ErrRoleCredsExpired,
}

// Fixed phrases the CLI or AWS prints without a usable code. Clock phrases come
// first: a skewed clock can also make an SSO token look expired. A bare
// SignatureDoesNotMatch is usually a wrong key, not the clock.
var phraseClasses = []struct {
	err     *SafeError
	markers []string
}{
	{ErrClockSkew, []string{"signature expired:", "signature not yet current:"}},
	{ErrSSOSignIn, []string{"token has expired and refresh failed", "session associated with this profile has expired", "error loading sso token", "pending authorization to retrieve an sso token has expired"}},
	{ErrNetwork, []string{"could not connect to the endpoint url", "connect timeout on endpoint url", "failed to connect to proxy url"}},
}

// classifyFailure maps CLI error output to a fixed message. It only matches
// markers; no part of the output is copied into the result. During sign-in,
// a denied or expired authorization means the browser step was not completed.
func classifyFailure(stderr []byte, login bool) *SafeError {
	text := strings.ToLower(string(stderr))
	for _, marker := range phraseClasses[0].markers {
		if strings.Contains(text, marker) {
			return ErrClockSkew
		}
	}
	if m := errorCode.FindSubmatch(stderr); m != nil {
		if class, ok := codeClasses[string(m[1])]; ok {
			if login && (class == ErrAccessDenied || class == ErrRoleCredsExpired) {
				return ErrSSOSignIn
			}
			return class
		}
	}
	for _, class := range phraseClasses[1:] {
		for _, marker := range class.markers {
			if strings.Contains(text, marker) {
				return class.err
			}
		}
	}
	return ErrRequestFailed
}
