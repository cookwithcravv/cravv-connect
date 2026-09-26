package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// acceptAll is a broken Verifier (think a PAM stack with pam_permit).
type acceptAll struct{ got []string }

func (a *acceptAll) Verify(_, pw string) error {
	a.got = append(a.got, pw)
	return nil
}

func TestSelfTestFailsWhenEverythingIsAccepted(t *testing.T) {
	a := &acceptAll{}
	err := SelfTest(a, "alice")
	if !errors.Is(err, ErrAcceptsAnyPassword) {
		t.Fatalf("SelfTest(accept-all) = %v, want ErrAcceptsAnyPassword", err)
	}
	if len(a.got) != 1 || len(a.got[0]) != 64 || strings.Trim(a.got[0], "0123456789abcdef") != "" {
		t.Fatalf("SelfTest tried %q, want one 64-char hex password", a.got)
	}
	if strings.Contains(err.Error(), a.got[0]) {
		t.Fatal("the probe password leaked into the error")
	}
}

func TestSelfTestPassesWhenRandomPasswordRejected(t *testing.T) {
	if err := SelfTest(Fake{Password: "correct horse"}, "alice"); err != nil {
		t.Fatalf("SelfTest(Fake) = %v, want nil", err)
	}
}

type failingVerifier struct{ err error }

func (f failingVerifier) Verify(string, string) error { return f.err }

// Only "wrong password" shows the check works. Any other error means the
// daemon cannot check passwords at all: it starts, but reports it.
func TestSelfTestReportsVerifierThatDoesNotWork(t *testing.T) {
	serviceErr := errors.New("pam authenticate: service error")
	for _, v := range []Verifier{unavailable{}, NewPAM("sudo"), failingVerifier{serviceErr}} {
		err := SelfTest(v, "alice")
		if !errors.Is(err, ErrCheckNotWorking) || errors.Is(err, ErrAcceptsAnyPassword) {
			t.Fatalf("SelfTest(%T) = %v, want ErrCheckNotWorking", v, err)
		}
	}
	if err := SelfTest(failingVerifier{serviceErr}, "alice"); !errors.Is(err, serviceErr) {
		t.Fatalf("cause not kept: %v", err)
	}
	// Authentication passed and only the account check refused: the stack
	// accepted the random password.
	if err := SelfTest(failingVerifier{fmt.Errorf("x: %w", ErrAccountRejected)}, "alice"); !errors.Is(err, ErrAcceptsAnyPassword) {
		t.Fatalf("account-rejected probe = %v, want ErrAcceptsAnyPassword", err)
	}
}

func TestSelfTestUsesAFreshPasswordEachTime(t *testing.T) {
	a := &acceptAll{}
	SelfTest(a, "alice")
	SelfTest(a, "alice")
	if len(a.got) != 2 || a.got[0] == a.got[1] {
		t.Fatalf("probe passwords %q are not fresh", a.got)
	}
}
