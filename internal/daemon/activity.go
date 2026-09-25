package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// OnlineWindow is how recently a peer must have sent something (any kind,
// including control.delivered receipts) to be shown as online.
const OnlineWindow = 10 * time.Minute

// PeerActivity remembers when each peer last sent us a handled envelope.
type PeerActivity struct {
	clock core.Clock
	mu    sync.Mutex
	seen  map[core.MachineID]time.Time
}

// NewPeerActivity builds an empty tracker.
func NewPeerActivity(clock core.Clock) *PeerActivity {
	return &PeerActivity{clock: clock, seen: map[core.MachineID]time.Time{}}
}

// Wrap decorates a handler so every envelope it receives marks the peer active.
func (a *PeerActivity) Wrap(h Handler) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
		a.mu.Lock()
		a.seen[peer.MachineID] = a.clock.Now()
		a.mu.Unlock()
		return h.Handle(ctx, peer, env)
	})
}

// WrapAll re-registers each kind's handler wrapped by Wrap.
func (a *PeerActivity) WrapAll(r *HandlerRegistry, kinds ...core.Kind) {
	for _, k := range kinds {
		if h, ok := r.Lookup(k); ok {
			r.Register(k, a.Wrap(h))
		}
	}
}

// Online reports whether the peer sent something within OnlineWindow.
func (a *PeerActivity) Online(id core.MachineID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.seen[id]
	return ok && a.clock.Now().Sub(t) <= OnlineWindow
}
