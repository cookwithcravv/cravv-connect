package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// ErrAcceptsAnyPassword is returned by SelfTest when the verifier accepted
// a random password.
var ErrAcceptsAnyPassword = errors.New("password verifier accepted a random password")

// ErrCheckNotWorking is returned by SelfTest when the verifier failed for a
// reason other than a wrong password (no PAM, a service error, a module
// that cannot run): passwords cannot be checked at all.
var ErrCheckNotWorking = errors.New("password check is not working")

// SelfTest checks that v rejects a random 32-byte hex password for username
// as a wrong password. It returns nil when it does; an error matching
// ErrAcceptsAnyPassword when the password is accepted (for example a PAM
// stack that ends in pam_permit, or one where only the account check
// refused), in which case the daemon must refuse to start; and an error
// matching ErrCheckNotWorking (and wrapping the cause) for any other
// failure, in which case the daemon starts but every unlock fails and status
// must say so. The probe password is never included in the error.
func SelfTest(v Verifier, username string) error {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("auth: self-test: random password: %w", err)
	}
	err := v.Verify(username, hex.EncodeToString(b[:]))
	switch {
	case errors.Is(err, core.ErrBadPassword):
		return nil
	case err == nil, errors.Is(err, ErrAccountRejected):
		return fmt.Errorf("auth: self-test for user %q: %w", username, ErrAcceptsAnyPassword)
	default:
		return fmt.Errorf("%w: %w", ErrCheckNotWorking, err)
	}
}
