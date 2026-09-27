package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// DiscoveryTimeout bounds how long List waits for a peer's sessions.listed.
const DiscoveryTimeout = 10 * time.Second

// ErrDiscoveryTimeout is returned when a peer did not answer sessions.list.
var ErrDiscoveryTimeout = errors.New("the machine did not answer in time: it may be offline")

// DirectSender sends one envelope right now, bypassing the outbox. Implemented by *Outbound.
type DirectSender interface {
	SendDirect(ctx context.Context, peer store.Peer, kind core.Kind, body any) error
}

// VisibleSessions lists the local sessions a peer may see. Implemented by *SessionService.
type VisibleSessions interface {
	Visible(ctx context.Context, peer core.MachineID) ([]store.SharedSession, error)
}

// Discovery answers sessions.list from paired machines and asks them for
// theirs (v2 spec 3.3). Both frames are ephemeral: sent directly, never
// queued, so a machine that is offline simply does not answer.
type Discovery struct {
	sessions VisibleSessions
	peers    PeerResolver
	sender   DirectSender
	clock    core.Clock
	limiter  *RateLimiter
	log      *slog.Logger
	timeout  time.Duration
	offers   OfferLister // nil: no offers are listed

	mu      sync.Mutex
	waiting map[discoveryKey]chan core.SessionsListedBody
}

type discoveryKey struct {
	peer  core.MachineID
	reqID string
}

// NewDiscovery wires a Discovery. log may be nil.
func NewDiscovery(sessions VisibleSessions, peers PeerResolver, sender DirectSender, clock core.Clock, log *slog.Logger) *Discovery {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Discovery{
		sessions: sessions, peers: peers, sender: sender, clock: clock, log: log, timeout: DiscoveryTimeout,
		limiter: NewRateLimiter(clock, core.DiscoveryPerMinute, time.Minute),
		waiting: map[discoveryKey]chan core.SessionsListedBody{},
	}
}

// List asks the machine (alias or machine ID) for the sessions it lets this
// machine see. It returns the peer too, so callers act on the same record.
func (d *Discovery) List(ctx context.Context, machine string) (store.Peer, core.SessionsListedBody, error) {
	peer, _, err := d.peers.Resolve(ctx, machine)
	if err != nil {
		return store.Peer{}, core.SessionsListedBody{}, err
	}
	if peer.Paused {
		return peer, core.SessionsListedBody{}, fmt.Errorf("%s: %w", peer.Alias, core.ErrPaused)
	}
	if peer.PausedByPeer {
		return peer, core.SessionsListedBody{}, fmt.Errorf("%s: %w", peer.Alias, core.ErrPausedByPeer)
	}
	key := discoveryKey{peer: peer.MachineID, reqID: core.NewIDAt(d.clock)}
	ch := make(chan core.SessionsListedBody, 1)
	d.mu.Lock()
	d.waiting[key] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.waiting, key)
		d.mu.Unlock()
	}()
	if err := d.sender.SendDirect(ctx, peer, core.KindSessionsList, core.SessionsListBody{ReqID: key.reqID}); err != nil {
		return peer, core.SessionsListedBody{}, fmt.Errorf("ask %s for its sessions: %w", peer.Alias, err)
	}
	timer := time.NewTimer(d.timeout)
	defer timer.Stop()
	select {
	case body := <-ch:
		return peer, body, nil
	case <-timer.C:
		return peer, core.SessionsListedBody{}, fmt.Errorf("%s: %w", peer.Alias, ErrDiscoveryTimeout)
	case <-ctx.Done():
		return peer, core.SessionsListedBody{}, ctx.Err()
	}
}

// HandleList answers sessions.list with the visible open and away sessions.
// At most core.DiscoveryPerMinute requests per peer are answered; the rest
// are dropped.
func (d *Discovery) HandleList(ctx context.Context, peer store.Peer, env core.Envelope) error {
	body, err := decodeEnvBody[core.SessionsListBody](env.Body)
	if err != nil {
		return err
	}
	if !core.ValidID(body.ReqID) {
		return fmt.Errorf("sessions.list: req_id: %w", errBadPeerID)
	}
	if !d.limiter.Allow(string(peer.MachineID)) {
		d.log.Info("sessions.list rate limited", "peer", peer.Alias)
		return nil
	}
	visible, err := d.sessions.Visible(ctx, peer.MachineID)
	if err != nil {
		return err
	}
	out := core.SessionsListedBody{ReqID: body.ReqID, Sessions: []core.ListedSession{}, Offers: []core.ListedOffer{}}
	for _, s := range visible {
		out.Sessions = append(out.Sessions, core.ListedSession{
			SessionID: s.ID, Name: s.Name, Purpose: s.Purpose, Kind: s.Kind, Agent: s.Agent, State: s.State,
		})
	}
	if out.Offers, err = d.listedOffers(ctx, peer.MachineID); err != nil {
		return err
	}
	return d.sender.SendDirect(ctx, peer, core.KindSessionsListed, out)
}

// HandleListed hands a peer's answer to the List call waiting for it.
// Answers nobody asked for are dropped; invalid entries are removed.
func (d *Discovery) HandleListed(_ context.Context, peer store.Peer, env core.Envelope) error {
	body, err := decodeEnvBody[core.SessionsListedBody](env.Body)
	if err != nil {
		return err
	}
	d.mu.Lock()
	ch, ok := d.waiting[discoveryKey{peer: peer.MachineID, reqID: body.ReqID}]
	d.mu.Unlock()
	if !ok {
		return nil
	}
	clean := core.SessionsListedBody{ReqID: body.ReqID, Sessions: []core.ListedSession{}, Offers: []core.ListedOffer{}}
	for _, s := range body.Sessions {
		if c, ok := cleanListedSession(s); ok {
			clean.Sessions = append(clean.Sessions, c)
		}
	}
	for _, o := range body.Offers {
		if c, ok := cleanListedOffer(o); ok {
			clean.Offers = append(clean.Offers, c)
		}
	}
	select {
	case ch <- clean:
	default: // a duplicate answer
	}
	return nil
}
