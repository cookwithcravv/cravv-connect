// Package auth verifies the OS login password for human-only actions and
// rate-limits attempts.
package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os/user"
	"runtime"
	"slices"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
)

// Verifier checks a username/password pair against the OS.
// It returns core.ErrBadPassword on a mismatch.
type Verifier interface {
	Verify(username, password string) error
}

// Fake is a Verifier for tests that accepts exactly Password.
type Fake struct{ Password string }

func (f Fake) Verify(_, password string) error {
	if subtle.ConstantTimeCompare([]byte(password), []byte(f.Password)) == 1 {
		return nil
	}
	return core.ErrBadPassword
}

// ErrUnavailable is returned by the verifier of a binary built without cgo.
var ErrUnavailable = errors.New("password auth unavailable (built without cgo)")

// unavailable is the Verifier used when PAM is not compiled in.
type unavailable struct{}

func (unavailable) Verify(string, string) error { return ErrUnavailable }

// ErrAccountRejected is returned when the password is correct but PAM's
// account management refuses the account (expired, disabled, or a password
// change is required). It is not a bad password and does not count toward
// the lockout.
var ErrAccountRejected = errors.New("account expired, disabled or not permitted")

// ErrServiceNotAllowed is returned (wrapped, together with ErrUnavailable)
// for a PAM service that is not on the allowlist. Only services that
// authenticate the user's own login password are allowed; others (for
// example "sudo" or "su") may be configured to accept anything or to do
// something other than a password check.
var ErrServiceNotAllowed = errors.New("pam service not allowed")

// allowedPAMServices lists the PAM services NewPAM accepts, per OS.
// "passwd" is deliberately absent: it is the password-change stack.
var allowedPAMServices = map[string][]string{
	"darwin": {"chkpasswd", "checkpw"},
	"linux":  {"login", "common-auth", "system-auth"},
}

// AllowedPAMServices returns the PAM services allowed on this OS.
func AllowedPAMServices() []string {
	return append([]string(nil), allowedPAMServices[runtime.GOOS]...)
}

// PAMServiceAllowed reports whether service is on this OS's allowlist
// (exact, case-sensitive match).
func PAMServiceAllowed(service string) bool {
	return slices.Contains(allowedPAMServices[runtime.GOOS], service)
}

// serviceNotAllowedError matches both ErrServiceNotAllowed and ErrUnavailable.
type serviceNotAllowedError struct{ service string }

func (e serviceNotAllowedError) Error() string {
	return fmt.Sprintf("auth: pam service not allowed: %q (allowed on %s: %s)",
		e.service, runtime.GOOS, strings.Join(AllowedPAMServices(), ", "))
}

func (e serviceNotAllowedError) Unwrap() []error {
	return []error{ErrServiceNotAllowed, ErrUnavailable}
}

// CheckPAMService returns nil if service is allowlisted, otherwise an error
// matching ErrServiceNotAllowed and ErrUnavailable.
func CheckPAMService(service string) error {
	if PAMServiceAllowed(service) {
		return nil
	}
	return serviceNotAllowedError{service: service}
}

// refused is the Verifier returned by NewPAM for a service that is not
// allowlisted: every check fails with err, which is never ErrBadPassword.
type refused struct{ err error }

func (r refused) Verify(string, string) error { return r.err }

// DefaultPAMService is the PAM service used when config.toml sets none:
// "chkpasswd" on macOS, "login" elsewhere.
func DefaultPAMService() string {
	if runtime.GOOS == "darwin" {
		return "chkpasswd"
	}
	return "login"
}

// DefaultVerifier returns the PAM verifier for DefaultPAMService, or the
// always-failing verifier when built without cgo.
func DefaultVerifier() Verifier { return NewPAM(DefaultPAMService()) }

// CurrentUsername returns the login name of the user running the daemon;
// the password check is always for this user.
func CurrentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("auth: current user: %w", err)
	}
	return u.Username, nil
}
