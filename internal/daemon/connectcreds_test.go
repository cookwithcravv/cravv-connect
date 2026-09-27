package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// settingsFailStore fails GetSetting while fail is set.
type settingsFailStore struct {
	store.Store
	fail atomic.Bool
}

func (s *settingsFailStore) GetSetting(ctx context.Context, key string) (string, bool, error) {
	if s.fail.Load() {
		return "", false, errors.New("database is locked")
	}
	return s.Store.GetSetting(ctx, key)
}

// When the one-time credentials cannot be read, an unregistered daemon does
// not dial with none (the relay would refuse it); it backs off and retries.
func TestConnectLoopBacksOffWhenCredentialsFail(t *testing.T) {
	relay := &d2Relay{}
	d := d2NewDaemon(t, t.TempDir(), relay)
	st := &settingsFailStore{Store: d.store}
	st.fail.Store(true)
	d.store = st
	d2Run(t, d)
	time.Sleep(100 * time.Millisecond)
	if n := relay.dials(); n != 0 {
		t.Fatalf("dialed %d times without readable credentials", n)
	}
	st.fail.Store(false)
	d2Eventually(t, "dial after the store recovers", func() bool { return relay.dials() >= 1 })
}

// A registered daemon needs no credentials, so it dials anyway.
func TestConnectLoopDialsWhenRegisteredDespiteCredentialError(t *testing.T) {
	relay := &d2Relay{}
	d := d2NewDaemon(t, t.TempDir(), relay)
	d.registered.Store(true)
	st := &settingsFailStore{Store: d.store}
	st.fail.Store(true)
	d.store = st
	d2Run(t, d)
	d2Eventually(t, "dial", func() bool { return relay.dials() >= 1 })
}
