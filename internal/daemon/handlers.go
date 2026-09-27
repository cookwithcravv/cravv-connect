package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// Handler processes one opened, verified, deduplicated envelope from a paired peer.
type Handler interface {
	Handle(ctx context.Context, peer store.Peer, env core.Envelope) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, peer store.Peer, env core.Envelope) error

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, peer store.Peer, env core.Envelope) error {
	return f(ctx, peer, env)
}

// HandlerRegistry maps message kinds to handlers. New kinds register here; Inbound never changes.
type HandlerRegistry struct {
	mu sync.RWMutex
	m  map[core.Kind]Handler
}

// NewHandlerRegistry returns an empty registry.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{m: make(map[core.Kind]Handler)}
}

// Register sets the handler for k, replacing any earlier one.
func (r *HandlerRegistry) Register(k core.Kind, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[k] = h
}

// Lookup returns the handler for k.
func (r *HandlerRegistry) Lookup(k core.Kind) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.m[k]
	return h, ok
}

// ControlOutbox is what the control handlers need from *Outbound.
type ControlOutbox interface {
	Resealer
	DeliveryMarker
	OutboxReleaser
}

// RegisterControlHandlers registers every control.* handler (called from wire.go).
func RegisterControlHandlers(r *HandlerRegistry, peers store.PeerStore, peerState PeerStateUpdater, out ControlOutbox) {
	r.Register(core.KindControlPrekey, NewPrekeyHandler(peers))
	r.Register(core.KindControlStalePrekey, NewStalePrekeyHandler(peers, out))
	r.Register(core.KindControlDelivered, NewDeliveredHandler(out))
	r.Register(core.KindControlPaused, NewPausedHandler(peerState))
	r.Register(core.KindControlResumed, NewResumedHandler(peerState, out))
	r.Register(core.KindControlUnpaired, NewUnpairedHandler(peerState))
	r.Register(core.KindControlRelayMoved, NewRelayMovedHandler(peers))
}

func decodeBody(env core.Envelope, v any) error {
	if err := json.Unmarshal(env.Body, v); err != nil {
		return fmt.Errorf("%s body: %w", env.Kind, err)
	}
	return nil
}

// updatePrekey stores w as the peer's prekey when it is signed by the peer's IK and newer
// than the one we hold. It reports whether the stored prekey changed.
func updatePrekey(ctx context.Context, peers store.PeerStore, id core.MachineID, w core.SignedPrekeyWire) (bool, error) {
	p, err := peers.GetPeer(ctx, id)
	if err != nil {
		return false, err
	}
	if err := keys.SignedPrekeyFromWire(w).Verify(p.IK); err != nil {
		return false, fmt.Errorf("prekey from %s: %w", p.Alias, err)
	}
	if w.ID == p.Prekey.ID || w.CreatedAt < p.Prekey.CreatedAt {
		return false, nil
	}
	p.Prekey = w
	return true, peers.PutPeer(ctx, p)
}

// NewPrekeyHandler handles control.prekey: verify against the peer's IK and store if newer.
func NewPrekeyHandler(peers store.PeerStore) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
		var b core.PrekeyBody
		if err := decodeBody(env, &b); err != nil {
			return err
		}
		_, err := updatePrekey(ctx, peers, peer.MachineID, b.Prekey)
		return err
	})
}

// NewStalePrekeyHandler handles control.stale_prekey: store the peer's current prekey and
// re-seal the named outbox item, but only if that item is addressed to this peer.
func NewStalePrekeyHandler(peers store.PeerStore, out Resealer) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
		var b core.StalePrekeyBody
		if err := decodeBody(env, &b); err != nil {
			return err
		}
		if _, err := updatePrekey(ctx, peers, peer.MachineID, b.Prekey); err != nil {
			return err
		}
		if err := out.Reseal(ctx, peer.MachineID, b.MsgID); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
		return nil
	})
}

// NewDeliveredHandler handles control.delivered: drop confirmed items from the outbox.
func NewDeliveredHandler(out DeliveryMarker) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
		var b core.DeliveredBody
		if err := decodeBody(env, &b); err != nil {
			return err
		}
		return out.MarkDelivered(ctx, peer.MachineID, b.IDs)
	})
}

// NewPausedHandler handles control.paused: mark the peer as pausing us and hold our outbox.
func NewPausedHandler(ps PeerStateUpdater) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, _ core.Envelope) error {
		return ps.MarkPausedByPeer(ctx, peer.MachineID, true)
	})
}

// NewResumedHandler handles control.resumed: clear the flag and release our held outbox.
func NewResumedHandler(ps PeerStateUpdater, out OutboxReleaser) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, _ core.Envelope) error {
		if err := ps.MarkPausedByPeer(ctx, peer.MachineID, false); err != nil {
			return err
		}
		if peer.Paused { // we paused them ourselves: keep holding
			return nil
		}
		return out.Release(ctx, peer.MachineID)
	})
}

// NewUnpairedHandler handles control.unpaired: remove the peer locally (audited as by_peer).
func NewUnpairedHandler(ps PeerStateUpdater) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, _ core.Envelope) error {
		return ps.RemoveByPeer(ctx, peer.MachineID)
	})
}

// NewRelayMovedHandler handles control.relay_moved: record the peer's new relay URL.
func NewRelayMovedHandler(peers store.PeerStore) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
		var b core.RelayMovedBody
		if err := decodeBody(env, &b); err != nil {
			return err
		}
		if err := validRelayURL(b.RelayURL); err != nil {
			return fmt.Errorf("relay_moved from %s: %w", peer.Alias, err)
		}
		p, err := peers.GetPeer(ctx, peer.MachineID)
		if err != nil {
			return err
		}
		p.RelayURL = b.RelayURL
		return peers.PutPeer(ctx, p)
	})
}
