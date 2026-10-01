package daemon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// internalInvite is a connection whose invite request the relay answers
// with internal, as on a connection that reached Cloudflare's depth limit.
type internalInvite struct{ *roomMailbox }

func (internalInvite) RequestInvite(context.Context) (string, error) {
	return "", fmt.Errorf("relay: internal: %w", transport.ErrRelayInternal)
}

// When the relay answers pairing's invite request with internal, the client
// replaces that connection; pairing waits for the new one and tries once more.
func TestPairingRetriesOnAFreshConnectionAfterInternal(t *testing.T) {
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "Alice's MacBook", true)
	a.slot.set(internalInvite{&roomMailbox{fakeMailbox: a.mb, rooms: rooms}})
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.slot.set(&roomMailbox{fakeMailbox: newFakeMailbox(nil), rooms: rooms})
	}()
	if _, _, err := a.svc.Start(context.Background(), true); err != nil {
		t.Fatalf("Start: %v, want success on the new connection", err)
	}
}

// Without a new connection in time, pairing reports the relay's error.
func TestPairingInternalWithoutReconnectFails(t *testing.T) {
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "Alice's MacBook", true)
	a.svc.reconnectWait = 200 * time.Millisecond
	a.slot.set(internalInvite{&roomMailbox{fakeMailbox: a.mb, rooms: rooms}})
	_, _, err := a.svc.Start(context.Background(), true)
	if !errors.Is(err, transport.ErrRelayInternal) {
		t.Fatalf("Start: %v, want ErrRelayInternal", err)
	}
}
