package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
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
// a peer has an active link, it is pinged every core.PresenceInterval; a link
// is dead after core.PresenceTimeout without fresh evidence, or as soon as a
// fresh pong leaves it out. Pings and pongs are ephemeral (sent directly).
type PresenceService struct {
	links  store.LinkStore
	peers  store.PeerStore
	closer PresenceCloser
	sender DirectSender
	clock  core.Clock
	log    *slog.Logger

	mu    sync.Mutex
	fresh map[linkKey]time.Time // last fresh evidence per active link
	pings map[core.MachineID][]sentPing
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
		fresh: map[linkKey]time.Time{}, pings: map[core.MachineID][]sentPing{},
	}
}

// Tick closes links without fresh evidence for core.PresenceTimeout, then
// pings every peer that still has an active link. The daemon calls it every
// core.PresenceInterval. A link seen for the first time (new, or after a
// restart) gets the full timeout from now.
func (p *PresenceService) Tick(ctx context.Context) error {
	active, err := p.links.ListLinks(ctx, store.LinkFilter{States: []store.LinkState{store.LinkActive}})
	if err != nil {
		return err
	}
	now := p.clock.Now()
	byPeer := map[core.MachineID][]string{}
	var dead []store.Link
	p.mu.Lock()
	live := make(map[linkKey]bool, len(active))
	for _, l := range active {
		k := linkKey{l.Peer, l.ID}
		live[k] = true
		last, ok := p.fresh[k]
		if !ok {
			p.fresh[k] = now
			last = now
		}
		if now.Sub(last) > core.PresenceTimeout {
			dead = append(dead, l)
			delete(p.fresh, k)
			continue
		}
		byPeer[l.Peer] = append(byPeer[l.Peer], l.ID)
	}
	for k := range p.fresh {
		if !live[k] {
			delete(p.fresh, k)
		}
	}
	p.mu.Unlock()
	for _, l := range dead {
		if err := p.closer.ClosePresence(ctx, l); err != nil {
			p.log.Warn("close timed-out link", "link", l.Num, "err", err)
		}
	}
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
			p.log.Debug("presence ping not sent", "peer", peer.Alias, "err", err)
		}
	}
	return nil
}

// HandlePing answers with the pinged links that are open here. A pending
// link counts as open: the peer may have accepted a request whose
// link.accepted has not arrived yet. A ping is also fresh evidence for the
// links it names that are active here.
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
			p.touch(linkKey{peer.MachineID, id})
		}
	}
	return p.sender.SendDirect(ctx, peer, core.KindPresencePong, core.PresencePongBody{TS: b.TS, LinkIDsOpen: open})
}

// HandlePong refreshes the links the peer still has open and closes the
// links the matching ping named that the pong leaves out.
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
		p.touch(linkKey{peer.MachineID, id})
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
