package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/core"
)

// The password lockout lives in the store, so restarting the daemon (or
// killing it to reset the counter) does not lift it.
func TestGuardLockoutSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	d := d2NewDaemon(t, dir, &d2Relay{})
	for i := 0; i < core.LockoutFailures; i++ {
		if err := d.Guard().Check("wrong"); !errors.Is(err, core.ErrBadPassword) {
			t.Fatalf("attempt %d err = %v, want ErrBadPassword", i+1, err)
		}
	}
	if err := d.Guard().Check("pw"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("after %d failures err = %v, want ErrLocked", core.LockoutFailures, err)
	}
	d.Close()

	d2 := d2NewDaemon(t, dir, &d2Relay{})
	defer d2.Close()
	if err := d2.Guard().Check("pw"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("after restart err = %v, want ErrLocked", err)
	}
}

// d2AcceptAll is a verifier that accepts every password, like a PAM stack
// ending in pam_permit.
type d2AcceptAll struct{}

func (d2AcceptAll) Verify(string, string) error { return nil }

// A verifier that accepts any password would make every password gate a
// no-op, so the daemon refuses to start with one.
func TestNewRefusesVerifierThatAcceptsAnyPassword(t *testing.T) {
	dir := t.TempDir()
	opts := d2Options(dir, &d2Relay{})
	opts.Verifier = d2AcceptAll{}
	d, err := New(opts)
	if err == nil {
		d.Close()
		t.Fatal("New accepted a verifier that accepts any password")
	}
	if !errors.Is(err, auth.ErrAcceptsAnyPassword) {
		t.Fatalf("New err = %v, want ErrAcceptsAnyPassword", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store opened before the verifier self-test: %v", err)
	}
}
