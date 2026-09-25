package daemon

import (
	"errors"
	"testing"

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
