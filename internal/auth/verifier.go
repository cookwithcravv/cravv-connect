// Package auth verifies the OS login password for human-only actions and
// rate-limits attempts.
package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os/user"
	"runtime"

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
