package daemon

import (
	"context"
	"crypto/ecdh"
	"errors"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/store"
)

// PrekeyManager owns our X25519 prekeys: creation, weekly rotation with broadcast, and purge.
type PrekeyManager struct {
	store    store.PrekeyStore
	peers    store.PeerStore
	identity *keys.Identity
	sender   EnvelopeSender
	clock    core.Clock
	mu       sync.Mutex // serializes generate/rotate
}

// NewPrekeyManager wires a PrekeyManager. sender is normally the *Outbound.
func NewPrekeyManager(ps store.PrekeyStore, peers store.PeerStore, id *keys.Identity, sender EnvelopeSender, clock core.Clock) *PrekeyManager {
	return &PrekeyManager{store: ps, peers: peers, identity: id, sender: sender, clock: clock}
}

// EnsureCurrent returns the current signed prekey, generating the first one if none exists.
func (m *PrekeyManager) EnsureCurrent(ctx context.Context) (keys.SignedPrekey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, err := m.store.CurrentPrekey(ctx)
	if errors.Is(err, core.ErrNotFound) {
		return m.generateLocked(ctx)
	}
	if err != nil {
		return keys.SignedPrekey{}, err
	}
	return m.sign(rec)
}

// Current returns the current signed prekey.
func (m *PrekeyManager) Current(ctx context.Context) (keys.SignedPrekey, error) {
	rec, err := m.store.CurrentPrekey(ctx)
	if err != nil {
		return keys.SignedPrekey{}, err
	}
	return m.sign(rec)
}

// PrivatePrekey implements sealing.PrekeyResolver. Superseded keys resolve until Purge deletes them.
func (m *PrekeyManager) PrivatePrekey(id string) (*ecdh.PrivateKey, bool) {
	rec, err := m.store.GetPrekey(context.Background(), id)
	if err != nil {
		return nil, false
	}
	pk, err := keys.PrekeyFromBytes(rec.ID, rec.Priv, rec.CreatedAt)
	if err != nil {
		return nil, false
	}
	return pk.Priv, true
}

// RotateIfDue replaces the current prekey once it is core.PrekeyRotation old, supersedes the
// others, and sends control.prekey to every peer. It reports whether it rotated.
// Paused peers are skipped by the sender (core.ErrPaused) and get the new key through
// control.stale_prekey on their next message.
func (m *PrekeyManager) RotateIfDue(ctx context.Context) (bool, error) {
	m.mu.Lock()
	rec, err := m.store.CurrentPrekey(ctx)
	switch {
	case errors.Is(err, core.ErrNotFound):
	case err != nil:
		m.mu.Unlock()
		return false, err
	case m.clock.Now().Sub(rec.CreatedAt) < core.PrekeyRotation:
		m.mu.Unlock()
		return false, nil
	}
	signed, err := m.generateLocked(ctx)
	m.mu.Unlock()
	if err != nil {
		return false, err
	}
	return true, m.broadcast(ctx, signed)
}

// Purge deletes private prekeys superseded more than core.PrekeyRetention ago.
func (m *PrekeyManager) Purge(ctx context.Context) (int, error) {
	return m.store.DeleteSupersededBefore(ctx, m.clock.Now().Add(-core.PrekeyRetention))
}

func (m *PrekeyManager) generateLocked(ctx context.Context) (keys.SignedPrekey, error) {
	now := m.clock.Now()
	pk, err := keys.GeneratePrekey(now)
	if err != nil {
		return keys.SignedPrekey{}, err
	}
	rec := store.PrekeyRecord{ID: pk.ID, Priv: pk.Priv.Bytes(), CreatedAt: now}
	if err := m.store.PutPrekey(ctx, rec); err != nil {
		return keys.SignedPrekey{}, err
	}
	if err := m.store.SupersedeAllExcept(ctx, pk.ID, now); err != nil {
		return keys.SignedPrekey{}, err
	}
	return pk.Signed(m.identity), nil
}

func (m *PrekeyManager) broadcast(ctx context.Context, signed keys.SignedPrekey) error {
	peers, err := m.peers.ListPeers(ctx)
	if err != nil {
		return err
	}
	body := core.PrekeyBody{Prekey: signed.Wire()}
	var errs []error
	for _, p := range peers {
		if p.Paused {
			continue
		}
		if _, err := m.sender.SendEnvelope(ctx, p.MachineID, core.KindControlPrekey, "", "", body); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *PrekeyManager) sign(rec store.PrekeyRecord) (keys.SignedPrekey, error) {
	pk, err := keys.PrekeyFromBytes(rec.ID, rec.Priv, rec.CreatedAt)
	if err != nil {
		return keys.SignedPrekey{}, err
	}
	return pk.Signed(m.identity), nil
}
