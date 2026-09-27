package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// gatedRelay holds dials without credentials until release is closed, then
// refuses them like a relay refuses an unregistered key.
type gatedRelay struct {
	d2Relay
	first   chan struct{} // closed when the first dial without credentials starts
	release chan struct{}
	once    sync.Once
}

func (r *gatedRelay) Dialer() transport.Dialer { return r }

func (r *gatedRelay) Dial(ctx context.Context, s transport.Signer, c transport.Credentials) (transport.Mailbox, error) {
	if c.Invite == "" && c.AdminToken == "" {
		r.once.Do(func() { close(r.first) })
		<-r.release
		return nil, transport.ErrRelayForbidden
	}
	return r.d2Relay.Dial(ctx, s, c)
}

// A dial without credentials that is refused after EnsureRegistered stored
// the invite must not abort the registration (found by the e2e pairing tests).
func TestEnsureRegisteredIgnoresRefusalOfEarlierDial(t *testing.T) {
	ctx := context.Background()
	relay := &gatedRelay{first: make(chan struct{}), release: make(chan struct{})}
	d := d2NewDaemon(t, t.TempDir(), relay)
	d2Run(t, d)
	<-relay.first // a dial without credentials is in flight

	errc := make(chan error, 1)
	go func() {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		errc <- d.EnsureRegistered(cctx, "INVITE-2")
	}()
	d2Eventually(t, "invite stored", func() bool {
		v, _, _ := d.Settings().GetSetting(ctx, SettingRelayInvite)
		return v == "INVITE-2"
	})
	time.Sleep(50 * time.Millisecond) // let EnsureRegistered reach its wait
	close(relay.release)              // the old dial is refused only now

	if err := <-errc; err != nil {
		t.Fatalf("EnsureRegistered: %v", err)
	}
	if !d.Registered() {
		t.Fatal("not registered")
	}
	if last := relay.cred(relay.dials() - 1); last.Invite != "INVITE-2" {
		t.Fatalf("last dial creds = %+v", last)
	}
}
