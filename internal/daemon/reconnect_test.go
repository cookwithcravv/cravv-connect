package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// flapRelay accepts every dial and drops the connection at once, like a
// relay that fails every request and closes the socket.
type flapRelay struct {
	d2Relay
	mu    sync.Mutex
	dials []time.Time
}

func (r *flapRelay) Dialer() transport.Dialer { return r }

func (r *flapRelay) Dial(ctx context.Context, s transport.Signer, c transport.Credentials) (transport.Mailbox, error) {
	mb, err := r.d2Relay.Dial(ctx, s, c)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.dials = append(r.dials, time.Now())
	r.mu.Unlock()
	mb.Close()
	return mb, nil
}

func (r *flapRelay) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dials)
}

// A connection that ends at once grows the backoff: no reconnect storm.
func TestReconnectBacksOffWhenConnectionsDropAtOnce(t *testing.T) {
	relay := &flapRelay{}
	d := d2NewDaemon(t, t.TempDir(), relay)
	stop := d2Run(t, d)
	time.Sleep(600 * time.Millisecond)
	stop()
	// ReconnectMin is 10 ms: doubling gives dials at about 0, 10, 30, 70,
	// 150, 310 and 630 ms. Without the backoff there are dozens.
	if n := relay.count(); n < 2 || n > 10 {
		t.Fatalf("%d dials in 600 ms, want a growing backoff (2 to 10)", n)
	}
}

func TestReconnectBackoffResetsOnlyAfterAStableConnection(t *testing.T) {
	b := &reconnectBackoff{min: time.Second, stable: time.Minute, cur: time.Second}
	for _, want := range []time.Duration{1, 2, 4, 8} {
		if got := b.next(); got != want*time.Second {
			t.Fatalf("next = %v, want %v", got, want*time.Second)
		}
	}
	b.ended(59 * time.Second) // too short: keeps growing
	if got := b.next(); got != 16*time.Second {
		t.Fatalf("after a short connection next = %v, want 16s", got)
	}
	b.ended(time.Minute)
	if got := b.next(); got != time.Second {
		t.Fatalf("after a stable connection next = %v, want 1s", got)
	}
	for range 20 {
		b.next()
	}
	if got := b.next(); got != core.BackoffMax {
		t.Fatalf("cap = %v, want %v", got, core.BackoffMax)
	}
}
