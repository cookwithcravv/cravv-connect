package daemon

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/bindcode"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/pake"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

var (
	// ErrPairingFailed covers a wrong code, a tampered exchange, or a peer that aborted.
	ErrPairingFailed = errors.New("pairing failed: wrong code or the exchange was interrupted")
	// ErrPairingExpired is returned for a pending pairing older than the room lifetime.
	ErrPairingExpired = errors.New("pairing request expired")
	// ErrPairingInProgress is returned by Finalize before the exchange has finished.
	ErrPairingInProgress = errors.New("pairing still in progress")
	// ErrOfflineForPairing is returned by Start when there is no live registered mailbox.
	ErrOfflineForPairing = errors.New("not connected to the relay")
	// ErrPairingClosed is returned by Start after Close (daemon shutdown or identity reset).
	ErrPairingClosed = errors.New("pairing service stopped")
)

// pairAAD binds the sealed messages to this protocol; pairAck is the final confirmation.
var (
	pairAAD = []byte("cravv-connect/pair-v1/payload")
	pairAck = []byte("cravv-connect/pair-v1/ok")
)

// PairingConfig is this machine's side of the exchange.
type PairingConfig struct {
	DeviceName string
	RelayURL   string
}

// Proposal is what the human sees before choosing an alias and trust level.
type Proposal struct {
	PendingID     string
	SuggestedName string // sanitized alias suggestion (never shown raw)
	MachineID     core.MachineID
}

// pairPayload is exchanged under the PAKE session key.
type pairPayload struct {
	IK       []byte                `json:"ik"`
	Prekey   core.SignedPrekeyWire `json:"prekey"`
	RelayURL string                `json:"relay_url"`
	Name     string                `json:"name"`
	Invite   string                `json:"invite,omitempty"` // creator (side A) only
}

type pendingPair struct {
	role     string // "creator" | "joiner"
	expires  int64  // unix ms, from the injected clock; guarded by PairingService.mu
	done     chan struct{}
	proposal Proposal
	peer     pairPayload
	err      error
}

// PairingService runs pair (creator) and join (joiner) over a relay room with SPAKE2,
// then stores the peer when the human finalizes.
type PairingService struct {
	identity  *keys.Identity
	rooms     transport.Rooms
	mailboxes MailboxProvider
	pake      pake.Factory
	peers     store.PeerStore
	prekeys   PrekeyProvider
	registrar Registrar
	cfg       PairingConfig
	clock     core.Clock
	audit     audit.Logger

	// base bounds background exchanges to the service's lifetime; Close cancels it.
	base context.Context
	stop context.CancelFunc

	mu      sync.Mutex
	pending map[string]*pendingPair
}

// NewPairingService wires a PairingService.
func NewPairingService(id *keys.Identity, rooms transport.Rooms, mailboxes MailboxProvider, pf pake.Factory,
	peers store.PeerStore, prekeys PrekeyProvider, registrar Registrar, cfg PairingConfig,
	clock core.Clock, lg audit.Logger) *PairingService {
	base, stop := context.WithCancel(context.Background())
	return &PairingService{
		identity: id, rooms: rooms, mailboxes: mailboxes, pake: pf, peers: peers, prekeys: prekeys,
		registrar: registrar, cfg: cfg, clock: clock, audit: lg,
		base: base, stop: stop,
		pending: make(map[string]*pendingPair),
	}
}

// Close stops every background exchange (their Await calls fail) and makes Start refuse.
func (s *PairingService) Close() { s.stop() }

// Start creates a room and an invite and returns the bind code. The exchange runs in the
// background until a joiner arrives or the room lifetime ends; Await waits for it.
func (s *PairingService) Start(ctx context.Context) (pendingID, code string, err error) {
	if s.base.Err() != nil {
		return "", "", ErrPairingClosed
	}
	mb, ok := s.mailboxes.Mailbox()
	if !ok {
		return "", "", ErrOfflineForPairing
	}
	mine, err := s.ownPayload(ctx)
	if err != nil {
		return "", "", err
	}
	invite, err := mb.RequestInvite(ctx)
	if err != nil {
		return "", "", fmt.Errorf("request invite: %w", err)
	}
	mine.Invite = invite
	nameplate, token, err := mb.CreateRoom(ctx)
	if err != nil {
		return "", "", fmt.Errorf("create pairing room: %w", err)
	}
	c, err := bindcode.NewCode(nameplate)
	if err != nil {
		return "", "", err
	}
	room, err := s.rooms.Open(ctx, nameplate, token)
	if err != nil {
		return "", "", fmt.Errorf("open pairing room: %w", err)
	}
	p := s.newPending("creator")
	id := p.proposal.PendingID
	go func() {
		// Detached from the IPC call; bounded by the room lifetime and the service's.
		rctx, cancel := context.WithTimeout(s.base, core.RoomTTL)
		defer cancel()
		defer room.Close()
		if err := room.WaitPeer(rctx); err != nil {
			s.complete(p, pairPayload{}, fmt.Errorf("%w: %v", ErrPairingFailed, err))
			return
		}
		peer, err := s.exchange(rctx, room, pake.SideA, c.String(), mine)
		s.complete(p, peer, err)
	}()
	return id, c.String(), nil
}

// Await blocks until the pending exchange started by Start finishes, fails, or expires.
func (s *PairingService) Await(ctx context.Context, pendingID string) (Proposal, error) {
	p, err := s.lookup(pendingID)
	if err != nil {
		return Proposal{}, err
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		return Proposal{}, ctx.Err()
	}
	if s.expired(p) {
		s.forget(pendingID)
		return Proposal{}, ErrPairingExpired
	}
	if p.err != nil {
		s.forget(pendingID)
		return Proposal{}, p.err
	}
	return p.proposal, nil
}

// Join runs the joiner side for a bind code. It needs no mailbox: the invite that lets this
// machine register arrives in the creator's payload and is used by Finalize.
func (s *PairingService) Join(ctx context.Context, code string) (Proposal, error) {
	c, err := bindcode.Parse(code)
	if err != nil {
		return Proposal{}, err
	}
	mine, err := s.ownPayload(ctx)
	if err != nil {
		return Proposal{}, err
	}
	room, err := s.rooms.Open(ctx, c.Nameplate, "")
	if err != nil {
		return Proposal{}, fmt.Errorf("%w: %v", ErrPairingFailed, err)
	}
	defer room.Close()
	p := s.newPending("joiner")
	if err := room.WaitPeer(ctx); err != nil {
		s.forget(p.proposal.PendingID)
		return Proposal{}, fmt.Errorf("%w: %v", ErrPairingFailed, err)
	}
	peer, err := s.exchange(ctx, room, pake.SideB, c.String(), mine)
	s.complete(p, peer, err)
	if err != nil {
		s.forget(p.proposal.PendingID)
		return Proposal{}, err
	}
	return p.proposal, nil
}

// Finalize stores the peer under alias with the chosen incoming trust, registers this
// machine's mailbox first if needed (joiner), allows the peer on the relay, and audits.
// It returns the alias actually used.
func (s *PairingService) Finalize(ctx context.Context, pendingID, alias string, trust core.TrustLevel) (string, error) {
	if !trust.Valid() {
		return "", fmt.Errorf("invalid trust level %d", int(trust))
	}
	p, err := s.lookup(pendingID)
	if err != nil {
		return "", err
	}
	select {
	case <-p.done:
	default:
		return "", ErrPairingInProgress
	}
	if s.expired(p) {
		s.forget(pendingID)
		return "", ErrPairingExpired
	}
	if p.err != nil {
		s.forget(pendingID)
		return "", p.err
	}
	clean := SanitizeAlias(alias)
	if clean == "" {
		clean = p.proposal.SuggestedName
	}
	if s.registrar != nil && !s.registrar.Registered() {
		if p.peer.Invite == "" {
			return "", errors.New("this machine has no relay mailbox and the peer sent no invite")
		}
		if err := s.registrar.EnsureRegistered(ctx, p.peer.Invite); err != nil {
			return "", fmt.Errorf("register mailbox: %w", err)
		}
	}
	peer := store.Peer{
		MachineID: p.proposal.MachineID,
		IK:        ed25519.PublicKey(p.peer.IK),
		Alias:     clean,
		TrustIn:   trust,
		Prekey:    p.peer.Prekey,
		RelayURL:  p.peer.RelayURL,
		PairedAt:  s.clock.Now(),
	}
	if err := s.peers.PutPeer(ctx, peer); err != nil {
		return "", err
	}
	if mb, ok := s.mailboxes.Mailbox(); ok {
		_ = mb.Allow(ctx, peer.IK) // otherwise SyncAllowList allows it on the next connect
	}
	s.forget(pendingID)
	_ = s.audit.Record(audit.Event{TS: s.clock.Now(), Type: audit.EvPair, Peer: peer.MachineID, Alias: clean,
		Detail: map[string]any{"trust": trust.String(), "role": p.role}})
	return clean, nil
}

// exchange runs the symmetric protocol. Both sides:
//  1. send the PAKE message, receive the peer's, derive the key;
//  2. send ConfirmTag(key, own side), receive the peer's tag and check it;
//  3. send the payload sealed with ChaCha20-Poly1305 under SessionKey(key)
//     (nonce 0 for A, 1 for B);
//  4. receive and open the peer's payload and verify it;
//  5. send a sealed ack (nonce 2 for A, 3 for B) and check the peer's, so a payload
//     tampered in either direction fails on both sides.
//
// Any failure returns ErrPairingFailed; the caller closes the room, which burns it.
func (s *PairingService) exchange(ctx context.Context, room transport.Room, side pake.Side, code string, mine pairPayload) (pairPayload, error) {
	fail := func(step string, err error) (pairPayload, error) {
		if err == nil {
			return pairPayload{}, fmt.Errorf("%w (%s)", ErrPairingFailed, step)
		}
		return pairPayload{}, fmt.Errorf("%w (%s: %v)", ErrPairingFailed, step, err)
	}
	peerSide := side.Other()
	ex, err := s.pake.New([]byte(code), side)
	if err != nil {
		return fail("pake", err)
	}
	if err := room.Send(ctx, ex.Message()); err != nil {
		return fail("send pake", err)
	}
	peerMsg, err := room.Recv(ctx)
	if err != nil {
		return fail("recv pake", err)
	}
	key, err := ex.Finish(peerMsg)
	if err != nil {
		return fail("pake", err)
	}
	if err := room.Send(ctx, pake.ConfirmTag(key, side)); err != nil {
		return fail("send confirm", err)
	}
	tag, err := room.Recv(ctx)
	if err != nil {
		return fail("recv confirm", err)
	}
	if !pake.CheckConfirm(key, peerSide, tag) {
		return fail("confirm", nil)
	}
	aead, err := chacha20poly1305.New(pake.SessionKey(key))
	if err != nil {
		return fail("aead", err)
	}
	plain, err := json.Marshal(mine)
	if err != nil {
		return fail("encode", err)
	}
	if err := room.Send(ctx, aead.Seal(nil, pairNonce(side, 0), plain, pairAAD)); err != nil {
		return fail("send payload", err)
	}
	sealed, err := room.Recv(ctx)
	if err != nil {
		return fail("recv payload", err)
	}
	opened, err := aead.Open(nil, pairNonce(peerSide, 0), sealed, pairAAD)
	if err != nil {
		return fail("payload", err)
	}
	var peer pairPayload
	if err := json.Unmarshal(opened, &peer); err != nil {
		return fail("payload", err)
	}
	if err := s.validatePeer(peer); err != nil {
		return fail("payload", err)
	}
	if err := room.Send(ctx, aead.Seal(nil, pairNonce(side, 1), pairAck, pairAAD)); err != nil {
		return fail("send ack", err)
	}
	ack, err := room.Recv(ctx)
	if err != nil {
		return fail("recv ack", err)
	}
	if got, err := aead.Open(nil, pairNonce(peerSide, 1), ack, pairAAD); err != nil || string(got) != string(pairAck) {
		return fail("ack", err)
	}
	return peer, nil
}

// pairNonce is unique per (sender, message): A uses 0 then 2, B uses 1 then 3.
func pairNonce(sender pake.Side, msg byte) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	n[len(n)-1] = 2 * msg
	if sender == pake.SideB {
		n[len(n)-1]++
	}
	return n
}

func (s *PairingService) validatePeer(p pairPayload) error {
	if len(p.IK) != ed25519.PublicKeySize {
		return errors.New("bad identity key")
	}
	if keys.MachineIDOf(p.IK) == s.identity.MachineID() {
		return errors.New("cannot pair with this machine itself")
	}
	if err := keys.SignedPrekeyFromWire(p.Prekey).Verify(p.IK); err != nil {
		return err
	}
	return validRelayURL(p.RelayURL)
}

func (s *PairingService) ownPayload(ctx context.Context) (pairPayload, error) {
	pk, err := s.prekeys.EnsureCurrent(ctx)
	if err != nil {
		return pairPayload{}, err
	}
	return pairPayload{
		IK:       s.identity.Public(),
		Prekey:   pk.Wire(),
		RelayURL: s.cfg.RelayURL,
		Name:     s.cfg.DeviceName,
	}, nil
}

func (s *PairingService) newPending(role string) *pendingPair {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	for id, p := range s.pending { // lazy sweep
		if now.UnixMilli() > p.expires {
			delete(s.pending, id)
		}
	}
	p := &pendingPair{
		role:     role,
		expires:  now.Add(core.RoomTTL).UnixMilli(),
		done:     make(chan struct{}),
		proposal: Proposal{PendingID: core.NewID()},
	}
	s.pending[p.proposal.PendingID] = p
	return p
}

// complete records the outcome. A successful exchange gets a fresh RoomTTL for the human
// to finalize, however long the joiner took to arrive.
func (s *PairingService) complete(p *pendingPair, peer pairPayload, err error) {
	if err == nil {
		s.mu.Lock()
		p.expires = s.clock.Now().Add(core.RoomTTL).UnixMilli()
		s.mu.Unlock()
		id := keys.MachineIDOf(peer.IK)
		name := SanitizeAlias(peer.Name)
		if name == "" {
			name = "peer-" + strings.ToLower(id.Short()[:6])
		}
		p.proposal.MachineID = id
		p.proposal.SuggestedName = name
		p.peer = peer
	}
	p.err = err
	close(p.done)
}

func (s *PairingService) lookup(id string) (*pendingPair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok {
		return nil, fmt.Errorf("pairing %q: %w", id, core.ErrNotFound)
	}
	return p, nil
}

func (s *PairingService) expired(p *pendingPair) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clock.Now().UnixMilli() > p.expires
}

func (s *PairingService) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, id)
}
