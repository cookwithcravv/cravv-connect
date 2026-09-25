//go:build !cgo || !(linux || darwin)

package auth

import (
	"errors"
	"testing"
)

func TestNoPAMBuildRefusesEveryPassword(t *testing.T) {
	if err := NewPAM("login").Verify("me", "pw"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if err := DefaultVerifier().Verify("me", "pw"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("DefaultVerifier err = %v, want ErrUnavailable", err)
	}
}
