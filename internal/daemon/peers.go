package daemon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

// PeerService owns the local view of paired peers: aliases, trust, pause, and unpair.
type PeerService struct {
	peers     store.PeerStore
	mailboxes MailboxProvider
	out       OutboxControl
	audit     audit.Logger
	clock     core.Clock

	mu          sync.Mutex
	pendingDeny map[core.MachineID]ed25519.PublicKey // removed peers to deny on next connect
	observers   []TrustObserver
}

// NewPeerService wires a PeerService. out is normally the *Outbound.
func NewPeerService(peers store.PeerStore, mailboxes MailboxProvider, out OutboxControl, lg audit.Logger, clock core.Clock) *PeerService {
	return &PeerService{
		peers:       peers,
		mailboxes:   mailboxes,
		out:         out,
		audit:       lg,
		clock:       clock,
		pendingDeny: make(map[core.MachineID]ed25519.PublicKey),
	}
}

// AddTrustObserver registers o to be told whenever a peer's trust is lowered.
func (s *PeerService) AddTrustObserver(o TrustObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, o)
}

// List returns every paired peer.
func (s *PeerService) List(ctx context.Context) ([]store.Peer, error) {
	return s.peers.ListPeers(ctx)
}

// Resolve parses "alias" or "alias/session" and returns the peer and the session part ("" for machine-wide).
// A full machine ID is accepted in place of the alias.
func (s *PeerService) Resolve(ctx context.Context, aliasOrAddr string) (store.Peer, string, error) {
	name, session, _ := strings.Cut(strings.TrimSpace(aliasOrAddr), "/")
	name = strings.ToLower(name)
	if name == "" {
		return store.Peer{}, "", fmt.Errorf("peer %q: %w", aliasOrAddr, core.ErrNotFound)
	}
	p, err := s.peers.GetPeerByAlias(ctx, name)
	if errors.Is(err, core.ErrNotFound) {
		p, err = s.peers.GetPeer(ctx, core.MachineID(name))
	}
	if err != nil {
		return store.Peer{}, "", fmt.Errorf("peer %q: %w", name, err)
	}
	return p, session, nil
}

// SetAlias renames a peer. The new alias is sanitized; it fails with ErrBadAlias when
// nothing usable remains and with store.ErrAliasTaken when another peer uses it.
func (s *PeerService) SetAlias(ctx context.Context, alias, newAlias string) error {
	p, _, err := s.Resolve(ctx, alias)
	if err != nil {
		return err
	}
	clean := SanitizeAlias(newAlias)
	if clean == "" {
		return ErrBadAlias
	}
	p.Alias = clean
	return s.peers.PutPeer(ctx, p)
}

// SetTrust changes what the peer may do on this machine. Raising needs unlocked=true
// (the IPC layer sets it after auth.unlock); lowering always works.
func (s *PeerService) SetTrust(ctx context.Context, alias string, level core.TrustLevel, unlocked bool) error {
	if !level.Valid() {
		return fmt.Errorf("invalid trust level %d", int(level))
	}
	p, _, err := s.Resolve(ctx, alias)
	if err != nil {
		return err
	}
	old := p.TrustIn
	if level == old {
		return nil
	}
	if level > old && !unlocked {
		return core.ErrAuthRequired
	}
	p.TrustIn = level
	if err := s.peers.PutPeer(ctx, p); err != nil {
		return err
	}
	s.record(audit.Event{Type: audit.EvTrust, Peer: p.MachineID, Alias: p.Alias,
		Detail: map[string]any{"from": old.String(), "to": level.String()}})
	if level < old {
		s.mu.Lock()
		obs := append([]TrustObserver(nil), s.observers...)
		s.mu.Unlock()
		for _, o := range obs {
			if err := o.TrustLowered(ctx, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// Pause stops traffic with a peer in both directions. control.paused is sent (best effort)
// before the relay allow-list entry is removed. When offline, the deny happens on the next
// connect through SyncAllowList, because the peer record is marked Paused.
func (s *PeerService) Pause(ctx context.Context, alias string) error {
	p, _, err := s.Resolve(ctx, alias)
	if err != nil {
		return err
	}
	if p.Paused {
		return nil
	}
	_ = s.out.SendDirect(ctx, p, core.KindControlPaused, core.EmptyBody{})
	if err := s.out.Hold(ctx, p.MachineID); err != nil {
		return err
	}
	p.Paused = true
	if err := s.peers.PutPeer(ctx, p); err != nil {
		return err
	}
	if mb, ok := s.mailboxes.Mailbox(); ok {
		_ = mb.Deny(ctx, p.IK) // a failure is repaired by SyncAllowList on the next connect
	}
	s.record(audit.Event{Type: audit.EvPause, Peer: p.MachineID, Alias: p.Alias})
	return nil
}

// Resume undoes Pause: allow-list entry restored, held outbox released, control.resumed sent.
func (s *PeerService) Resume(ctx context.Context, alias string) error {
	p, _, err := s.Resolve(ctx, alias)
	if err != nil {
		return err
	}
	if !p.Paused {
		return nil
	}
	p.Paused = false
	if err := s.peers.PutPeer(ctx, p); err != nil {
		return err
	}
	if mb, ok := s.mailboxes.Mailbox(); ok {
		_ = mb.Allow(ctx, p.IK)
	}
	if _, err := s.out.SendEnvelope(ctx, p.MachineID, core.KindControlResumed, "", "", core.EmptyBody{}); err != nil {
		return err
	}
	if !p.PausedByPeer {
		if err := s.out.Release(ctx, p.MachineID); err != nil {
			return err
		}
	}
	s.record(audit.Event{Type: audit.EvResume, Peer: p.MachineID, Alias: p.Alias})
	return nil
}

// Unpair sends control.unpaired (best effort), removes the peer from the allow-list,
// and deletes the peer and its outbox. Pairing again needs a new bind code.
func (s *PeerService) Unpair(ctx context.Context, alias string) error {
	p, _, err := s.Resolve(ctx, alias)
	if err != nil {
		return err
	}
	_ = s.out.SendDirect(ctx, p, core.KindControlUnpaired, core.EmptyBody{})
	return s.remove(ctx, p, false)
}

// RemoveByPeer handles a control.unpaired from the peer itself: same cleanup as Unpair, no notice back.
func (s *PeerService) RemoveByPeer(ctx context.Context, id core.MachineID) error {
	p, err := s.peers.GetPeer(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.remove(ctx, p, true)
}

func (s *PeerService) remove(ctx context.Context, p store.Peer, byPeer bool) error {
	denied := false
	if mb, ok := s.mailboxes.Mailbox(); ok {
		denied = mb.Deny(ctx, p.IK) == nil
	}
	if !denied {
		s.mu.Lock()
		s.pendingDeny[p.MachineID] = p.IK
		s.mu.Unlock()
	}
	if err := s.out.Forget(ctx, p.MachineID); err != nil {
		return err
	}
	if err := s.peers.DeletePeer(ctx, p.MachineID); err != nil {
		return err
	}
	s.record(audit.Event{Type: audit.EvUnpair, Peer: p.MachineID, Alias: p.Alias,
		Detail: map[string]any{"by_peer": byPeer}})
	return nil
}

// MarkPausedByPeer records that the peer paused (true) or resumed (false) us.
// Pausing also holds our outbox for that peer; releasing is the caller's job (Outbound.Release).
func (s *PeerService) MarkPausedByPeer(ctx context.Context, id core.MachineID, paused bool) error {
	p, err := s.peers.GetPeer(ctx, id)
	if err != nil {
		return err
	}
	if paused {
		if err := s.out.Hold(ctx, id); err != nil {
			return err
		}
	}
	if p.PausedByPeer == paused {
		return nil
	}
	p.PausedByPeer = paused
	return s.peers.PutPeer(ctx, p)
}

// SyncAllowList makes the relay allow-list match local state. Call it on every connect.
func (s *PeerService) SyncAllowList(ctx context.Context, mb transport.Mailbox) error {
	peers, err := s.peers.ListPeers(ctx)
	if err != nil {
		return err
	}
	var errs []error
	current := make(map[core.MachineID]bool, len(peers))
	for _, p := range peers {
		current[p.MachineID] = true
		if p.Paused {
			errs = append(errs, mb.Deny(ctx, p.IK))
		} else {
			errs = append(errs, mb.Allow(ctx, p.IK))
		}
	}
	s.mu.Lock()
	pending := make(map[core.MachineID]ed25519.PublicKey, len(s.pendingDeny))
	for id, ik := range s.pendingDeny {
		pending[id] = ik
	}
	s.mu.Unlock()
	for id, ik := range pending {
		if current[id] { // paired again since the unpair: the loop above already allowed it
			s.mu.Lock()
			delete(s.pendingDeny, id)
			s.mu.Unlock()
			continue
		}
		if err := mb.Deny(ctx, ik); err != nil {
			errs = append(errs, err)
			continue
		}
		s.mu.Lock()
		delete(s.pendingDeny, id)
		s.mu.Unlock()
	}
	return errors.Join(errs...)
}

func (s *PeerService) record(e audit.Event) {
	if e.TS.IsZero() {
		e.TS = s.clock.Now()
	}
	_ = s.audit.Record(e)
}
