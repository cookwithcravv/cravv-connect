//go:build cgo && (linux || darwin)

package auth

import "testing"

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
