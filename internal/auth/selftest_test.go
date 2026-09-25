package auth

import (
	"errors"
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
	// A verifier that cannot check passwords at all rejects the probe too.
	if err := SelfTest(unavailable{}, "alice"); err != nil {
		t.Fatalf("SelfTest(unavailable) = %v, want nil", err)
	}
	if err := SelfTest(NewPAM("sudo"), "alice"); err != nil {
		t.Fatalf("SelfTest(refused) = %v, want nil", err)
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
