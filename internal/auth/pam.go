//go:build cgo && (linux || darwin)

package auth

import (
	"errors"
	"fmt"

	"github.com/msteinert/pam/v2"

	"github.com/cravv/cravv-connect/internal/core"
)

// pamVerifier authenticates through PAM (OpenPAM on macOS, Linux-PAM on Linux).
type pamVerifier struct{ service string }

// NewPAM returns a Verifier that authenticates against the given PAM service.
func NewPAM(service string) Verifier { return pamVerifier{service: service} }

func (v pamVerifier) Verify(username, password string) error {
	tx, err := pam.StartFunc(v.service, username, func(s pam.Style, _ string) (string, error) {
		switch s {
		case pam.PromptEchoOff:
			return password, nil
		case pam.PromptEchoOn:
			return username, nil
		case pam.ErrorMsg, pam.TextInfo:
			return "", nil
		default:
			return "", errors.New("unsupported PAM conversation style")
		}
	})
	if err != nil {
		return fmt.Errorf("auth: pam start %q: %w", v.service, err)
	}
	defer tx.End()
	if err := tx.Authenticate(pam.DisallowNullAuthtok); err != nil {
		if isBadCredential(err) {
			return core.ErrBadPassword
		}
		return fmt.Errorf("auth: pam authenticate: %w", err)
	}
	return nil
}

// isBadCredential reports whether a PAM error means "wrong user or password"
// rather than a broken PAM setup.
func isBadCredential(err error) bool {
	for _, e := range []pam.Error{pam.ErrAuth, pam.ErrUserUnknown, pam.ErrMaxtries, pam.ErrCredInsufficient, pam.ErrPermDenied} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
