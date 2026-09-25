package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrAcceptsAnyPassword is returned by SelfTest when the verifier accepted
// a random password.
var ErrAcceptsAnyPassword = errors.New("password verifier accepted a random password")

// SelfTest checks that v rejects a random 32-byte hex password for
// username. It returns an error matching ErrAcceptsAnyPassword if the
// password is accepted (for example a PAM stack that ends in pam_permit),
// in which case the daemon must refuse to start. Any rejection, including
// ErrUnavailable, passes: this only guards against accepting everything.
// The probe password is never included in the error.
func SelfTest(v Verifier, username string) error {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("auth: self-test: random password: %w", err)
	}
	if err := v.Verify(username, hex.EncodeToString(b[:])); err != nil {
		return nil
	}
	return fmt.Errorf("auth: self-test for user %q: %w", username, ErrAcceptsAnyPassword)
}
