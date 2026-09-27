//go:build cgo && (linux || darwin)

package auth

import (
	"errors"
	"fmt"

	"github.com/msteinert/pam/v2"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// pamVerifier authenticates through PAM (OpenPAM on macOS, Linux-PAM on Linux).
type pamVerifier struct{ service string }

// NewPAM returns a Verifier that authenticates against the given PAM
// service. A service that is not allowlisted (see AllowedPAMServices) yields
// a Verifier that never reaches PAM and fails every check with an error
// matching ErrServiceNotAllowed and ErrUnavailable.
func NewPAM(service string) Verifier {
	if err := CheckPAMService(service); err != nil {
		return refused{err: err}
	}
	return pamVerifier{service: service}
}

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
	if err := setPAMItems(tx); err != nil {
		return fmt.Errorf("auth: pam set item: %w", err)
	}
	if err := tx.Authenticate(pam.DisallowNullAuthtok); err != nil {
		if isBadCredential(err) {
			return core.ErrBadPassword
		}
		return fmt.Errorf("auth: pam authenticate: %w", err)
	}
	// The password is right; now ask the account stack whether the account
	// may be used at all (expired, disabled, password must be changed).
	return accountError(tx.AcctMgmt(pam.DisallowNullAuthtok))
}

// itemSetter is the part of *pam.Transaction setPAMItems uses.
type itemSetter interface {
	SetItem(pam.Item, string) error
}

// setPAMItems names a tty before authenticating. Some stacks need one: on
// Debian, login's pam_securetty fails with PAM_SERVICE_ERR when PAM_TTY is
// unset. The value is not a real terminal, so securetty-style rules that
// allow only listed ttys (root logins) still refuse.
func setPAMItems(tx itemSetter) error {
	return tx.SetItem(pam.Tty, "cravv-connect")
}

// accountError maps a pam_acct_mgmt result: nil stays nil, anything else is
// ErrAccountRejected wrapping the PAM error.
func accountError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("auth: pam account check: %w: %w", ErrAccountRejected, err)
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
