package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// maxPingLinks caps the link IDs one ping may ask about.
const maxPingLinks = 1000

// pingHistory is how many recent pings per peer are remembered to match pongs.
const pingHistory = 8

// PresenceCloser closes a link that timed out. Implemented by *LinkService.
type PresenceCloser interface {
	ClosePresence(ctx context.Context, l store.Link) error
}

// PresenceService keeps links honest when a machine drops (v2 spec 5): while
// a peer has an active link, it is pinged every core.PresenceInterval. A link
// without fresh evidence for core.PresenceTimeout is marked away
// (store.Link.PresenceAway), and it closes (presence_timeout) only when it
// stays away for its grace: core.AwayGrace, or a managed session's idle
// timeout (SetGrace). A fresh pong or traffic on the link makes it active
// again. A fresh pong that leaves a link out closes it at once (the peer no
// longer has it). Pings and pongs are ephemeral (sent directly).
type PresenceService struct {
	links  store.LinkStore
	peers  store.PeerStore
	closer PresenceCloser
	sender DirectSender
	clock  core.Clock
	log    *slog.Logger

	mu       sync.Mutex
	fresh    map[linkKey]time.Time // last fresh evidence per active link
	pings    map[core.MachineID][]sentPing
	pingLeft map[core.MachineID]bool // peers whose ping of the last round left this machine
	lastTick time.Time               // when Tick last ran (zero before the first)
	grace    func(ctx context.Context, l store.Link) time.Duration
	online   func() bool // whether this machine's relay mailbox is live; nil means always
}

type linkKey struct {
	peer core.MachineID
	id   string
}

type sentPing struct {
	ts  int64
	ids []string
}

// NewPresenceService wires the service. log may be nil.
func NewPresenceService(links store.LinkStore, peers store.PeerStore, closer PresenceCloser, sender DirectSender, clock core.Clock, log *slog.Logger) *PresenceService {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &PresenceService{
		links: links, peers: peers, closer: closer, sender: sender, clock: clock, log: log,
		fresh: map[linkKey]time.Time{}, pings: map[core.MachineID][]sentPing{}, pingLeft: map[core.MachineID]bool{},
	}
}

// SetOnline tells the service how to see whether this machine's relay
// mailbox is live. While it is not, silence proves nothing about a peer.
func (p *PresenceService) SetOnline(online func() bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.online = online
}

// SetGrace sets how long a link may stay away before it closes (default
// core.AwayGrace for every link).
func (p *PresenceService) SetGrace(grace func(ctx context.Context, l store.Link) time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grace = grace
}

func (p *PresenceService) graceOf(ctx context.Context, l store.Link) time.Duration {
	p.mu.Lock()
	grace := p.grace
	p.mu.Unlock()
	if grace == nil {
		return core.AwayGrace
	}
	return grace(ctx, l)
}

// sleepGap is how long since the last round counts as this machine having
// slept (or been stopped): then nothing it heard is fresh evidence either way.
const sleepGap = 2 * core.PresenceInterval

// elapsed is the time between two readings, by the monotonic clock and by
// the wall clock, whichever is longer (a sleeping Mac's monotonic clock
// stands still, its wall clock does not).
func elapsed(now, last time.Time) time.Duration {
	return max(now.Sub(last), now.Round(0).Sub(last.Round(0)))
}

// Tick marks links without fresh evidence for core.PresenceTimeout away,
// closes links that stayed away longer than their grace, then pings every
// peer with an active link (away ones too, so a machine that comes back is
// seen). The daemon calls it every core.PresenceInterval. A link seen for
// the first time (new, or after a restart) gets the full timeout from now.
// After a local sleep (more than two heartbeats since the last round) every
// link gets the full timeout from now and nothing is closed this round:
// the pings go out first. The same holds for a peer this machine could not
// hear from: while its own mailbox is not live, and for a peer whose ping
// of the last round did not leave (offline, or the relay refused it). Only
// silence after a ping that left, while connected, counts against a link,
// so a machine that is itself offline never closes its links.
func (p *PresenceService) Tick(ctx context.Context) error {
	active, err := p.links.ListLinks(ctx, store.LinkFilter{States: []store.LinkState{store.LinkActive}})
	if err != nil {
		return err
	}
	now := p.clock.Now()
	byPeer := map[core.MachineID][]string{}
	var silent, away []store.Link
	p.mu.Lock()
	slept := !p.lastTick.IsZero() && elapsed(now, p.lastTick) > sleepGap
	offline := p.online != nil && !p.online()
	p.lastTick = now
	live := make(map[linkKey]bool, len(active))
	for _, l := range active {
		k := linkKey{l.Peer, l.ID}
		live[k] = true
		byPeer[l.Peer] = append(byPeer[l.Peer], l.ID)
		blind := slept || offline || !p.pingLeft[l.Peer]
		last, ok := p.fresh[k]
		if !ok || blind {
			p.fresh[k] = now
			last = now
		}
		switch {
		case blind:
		case !l.PresenceAway.IsZero():
			away = append(away, l)
		case now.Sub(last) > core.PresenceTimeout:
			silent = append(silent, l)
		}
	}
	for k := range p.fresh {
		if !live[k] {
			delete(p.fresh, k)
		}
	}
	p.mu.Unlock()
	if slept {
		p.log.Info("presence: this machine was asleep; pinging before timing links out")
	}
	if offline {
		p.log.Info("presence: this machine is not connected to the relay; not timing links out")
	}
	for _, l := range silent {
		if err := p.markAway(ctx, l, now); err != nil {
			p.log.Warn("mark silent link away", "link", l.Num, "err", err)
		}
	}
	for _, l := range away {
		// The store keeps milliseconds: compare at that precision.
		if now.Truncate(time.Millisecond).Sub(l.PresenceAway.Truncate(time.Millisecond)) <= p.graceOf(ctx, l) {
			continue
		}
		p.mu.Lock()
		delete(p.fresh, linkKey{l.Peer, l.ID})
		p.mu.Unlock()
		if err := p.closer.ClosePresence(ctx, l); err != nil {
			p.log.Warn("close link away too long", "link", l.Num, "err", err)
		}
	}
	left := make(map[core.MachineID]bool, len(byPeer))
	defer func() {
		p.mu.Lock()
		p.pingLeft = left
		p.mu.Unlock()
	}()
	for id, ids := range byPeer {
		peer, err := p.peers.GetPeer(ctx, id)
		if err != nil || peer.Paused || peer.PausedByPeer {
			continue
		}
		ping := sentPing{ts: now.UnixMilli(), ids: ids}
		p.mu.Lock()
		h := append(p.pings[id], ping)
		if len(h) > pingHistory {
			h = h[len(h)-pingHistory:]
		}
		p.pings[id] = h
		p.mu.Unlock()
		if err := p.sender.SendDirect(ctx, peer, core.KindPresencePing, core.PresencePingBody{TS: ping.ts, LinkIDs: ids}); err != nil {
			p.log.Info("presence ping not sent; its silence will not count", "peer", peer.Alias, "err", err)
			continue
		}
		left[id] = true
	}
	return nil
}

// markAway records that the link's peer stopped answering at now.
func (p *PresenceService) markAway(ctx context.Context, l store.Link, now time.Time) error {
	_, err := p.links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
		if x.State == store.LinkActive && x.PresenceAway.IsZero() {
			x.PresenceAway = now
		}
		return nil
	})
	if err == nil {
		p.log.Info("presence: peer stopped answering; link away", "link", l.Num)
	}
	return err
}

// back records fresh evidence for a link active here and, if it was away,
// makes it active again.
func (p *PresenceService) back(ctx context.Context, l store.Link) {
	p.touch(linkKey{l.Peer, l.ID})
	if l.PresenceAway.IsZero() {
		return
	}
	if _, err := p.links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
		x.PresenceAway = time.Time{}
		return nil
	}); err != nil {
		p.log.Warn("clear link away", "link", l.Num, "err", err)
		return
	}
	p.log.Info("presence: peer answers again; link active", "link", l.Num)
}

// Traffic is told about every envelope the link gate admitted on active
// link l: the peer is there, so l is fresh and no longer away.
func (p *PresenceService) Traffic(ctx context.Context, l store.Link) {
	if l.State == store.LinkActive {
		p.back(ctx, l)
	}
}

// HandlePing answers with the pinged links that are open here. A pending
// link counts as open: the peer may have accepted a request whose
// link.accepted has not arrived yet. A ping is also fresh evidence for the
// links it names that are active here (an away one is active again).
func (p *PresenceService) HandlePing(ctx context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.PresencePingBody](env.Body)
	if err != nil {
		return err
	}
	if !p.freshTS(b.TS) || len(b.LinkIDs) > maxPingLinks {
		return nil
	}
	open := []string{}
	for _, id := range b.LinkIDs {
		l, err := p.links.GetLink(ctx, peer.MachineID, id)
		if err != nil || !l.Open() {
			continue
		}
		open = append(open, id)
		if l.State == store.LinkActive {
			p.back(ctx, l)
		}
	}
	return p.sender.SendDirect(ctx, peer, core.KindPresencePong, core.PresencePongBody{TS: b.TS, LinkIDsOpen: open})
}

// HandlePong refreshes the links the peer still has open (an away one is
// active again) and closes the links the matching ping named that the pong
// leaves out.
func (p *PresenceService) HandlePong(ctx context.Context, peer store.Peer, env core.Envelope) error {
	b, err := decodeEnvBody[core.PresencePongBody](env.Body)
	if err != nil {
		return err
	}
	if !p.freshTS(b.TS) {
		return nil
	}
	open := make(map[string]bool, len(b.LinkIDsOpen))
	for _, id := range b.LinkIDsOpen {
		open[id] = true
		if l, err := p.links.GetLink(ctx, peer.MachineID, id); err == nil && l.State == store.LinkActive {
			p.back(ctx, l)
		}
	}
	p.mu.Lock()
	var asked []string
	for _, s := range p.pings[peer.MachineID] {
		if s.ts == b.TS {
			asked = s.ids
		}
	}
	p.mu.Unlock()
	for _, id := range asked {
		if open[id] {
			continue
		}
		l, err := p.links.GetLink(ctx, peer.MachineID, id)
		if err != nil || l.State != store.LinkActive {
			continue
		}
		p.mu.Lock()
		delete(p.fresh, linkKey{peer.MachineID, id})
		p.mu.Unlock()
		if err := p.closer.ClosePresence(ctx, l); err != nil {
			p.log.Warn("close link the peer does not have", "link", l.Num, "err", err)
		}
	}
	return nil
}

// touch records fresh evidence for a link this side has active.
func (p *PresenceService) touch(k linkKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fresh[k] = p.clock.Now()
}

// freshTS reports whether a presence timestamp (unix ms) is at most
// core.PresenceMaxAge old and not more than core.MaxClockSkew ahead.
func (p *PresenceService) freshTS(ms int64) bool {
	ts := time.UnixMilli(ms)
	now := p.clock.Now()
	return now.Sub(ts) <= core.PresenceMaxAge && ts.Sub(now) <= core.MaxClockSkew
}

// ManagedLookup finds what the daemon keeps about a managed session.
// Implemented by store.OfferStore.
type ManagedLookup interface {
	GetManaged(ctx context.Context, sessionID string) (store.ManagedSession, error)
}

// OfferLookup finds an offer rule. Implemented by *OfferService.
type OfferLookup interface {
	Get(ctx context.Context, id string) (store.Offer, error)
}

// presenceGrace is how long a link may stay away before presence closes it
// (v2 spec 5): core.AwayGrace for a live session's link, and a managed
// session's idle timeout (its offer's, or the default when the offer is
// gone) for a managed one, so a machine that drops never ends a managed
// session sooner than idleness would.
func presenceGrace(sessions SessionLookup, managed ManagedLookup, offers OfferLookup) func(ctx context.Context, l store.Link) time.Duration {
	return func(ctx context.Context, l store.Link) time.Duration {
		s, err := sessions.Get(ctx, l.Session)
		if err != nil || s.Kind != core.SessionManaged {
			return core.AwayGrace
		}
		if m, err := managed.GetManaged(ctx, l.Session); err == nil {
			if o, err := offers.Get(ctx, m.OfferID); err == nil && o.IdleTimeout > 0 {
				return o.IdleTimeout
			}
		}
		return core.DefaultIdleTimeout
	}
}
