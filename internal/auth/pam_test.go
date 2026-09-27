//go:build cgo && (linux || darwin)

package auth

import (
	"errors"
	"testing"

	"github.com/msteinert/pam/v2"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// A real password cannot be tested here. This checks that the PAM verifier
// is wired in and rejects a user that cannot exist, without hanging.
func TestPAMRejectsUnknownUser(t *testing.T) {
	v := NewPAM(DefaultPAMService())
	if _, ok := v.(pamVerifier); !ok {
		t.Fatalf("NewPAM returned %T, want pamVerifier in a cgo build", v)
	}
	if err := v.Verify("cravv-no-such-user-7f3a", "not-a-password"); err == nil {
		t.Fatal("PAM accepted a nonexistent user")
	}
}

// A correct password for an expired, disabled or must-change account is
// refused with ErrAccountRejected (not ErrBadPassword: the password itself
// was right, and it must not count toward the lockout).
func TestPAMAccountErrorMapping(t *testing.T) {
	for _, pe := range []pam.Error{pam.ErrAcctExpired, pam.ErrNewAuthtokReqd, pam.ErrPermDenied, pam.ErrAuthtokExpired, pam.ErrUserUnknown} {
		err := accountError(pe)
		if !errors.Is(err, ErrAccountRejected) || !errors.Is(err, pe) {
			t.Fatalf("accountError(%v) = %v, want ErrAccountRejected wrapping it", pe, err)
		}
		if errors.Is(err, core.ErrBadPassword) {
			t.Fatalf("accountError(%v) counts as a bad password", pe)
		}
	}
	if err := accountError(nil); err != nil {
		t.Fatalf("accountError(nil) = %v", err)
	}
}

type recordedItems map[pam.Item]string

func (r recordedItems) SetItem(i pam.Item, v string) error { r[i] = v; return nil }

// Debian's login stack runs pam_securetty, which fails with PAM_SERVICE_ERR
// when PAM_TTY is unset: the verifier names a tty before authenticating.
func TestPAMSetsTTY(t *testing.T) {
	items := recordedItems{}
	if err := setPAMItems(items); err != nil {
		t.Fatal(err)
	}
	if items[pam.Tty] != "cravv-connect" {
		t.Fatalf("PAM_TTY = %q", items[pam.Tty])
	}
}
