package daemon

import (
	"context"
	"os"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/store"
)

// assembleManaged builds the offer rules and the SessionHost (they outlive
// ResetIdentity, like the shared sessions).
func (d *Daemon) assembleManaged(db store.Store, lg audit.Logger) {
	home, _ := os.UserHomeDir()
	d.offers = NewOfferService(db, currentPeers{d}, FolderRules{Home: home, StateDir: d.opts.Paths.Home}, d.clock, lg)
	d.host = NewSessionHost(HostDeps{Offers: d.offers, Store: db, Sessions: d.shared, Clock: d.clock, Audit: lg, Log: d.log})
	d.offers.AddObserver(d.host)
}

// buildManaged connects the identity-bound services to them: discovery
// lists the offers, a closed link closes its managed session, and
// unpairing a machine removes its offers.
func (d *Daemon) buildManaged(g *services) {
	g.discover.SetOffers(d.offers)
	g.links.AddCloseObserver(d.host)
	g.peers.AddCutOffObserver(d.offers)
}

// currentPeers resolves peers with the current PeerService (ResetIdentity
// replaces it).
type currentPeers struct{ d *Daemon }

func (p currentPeers) Resolve(ctx context.Context, addr string) (store.Peer, string, error) {
	return p.d.svc.Load().peers.Resolve(ctx, addr)
}

// Offers returns the managed-session offer rules.
func (d *Daemon) Offers() *OfferService { return d.offers }

// Host returns the SessionHost.
func (d *Daemon) Host() *SessionHost { return d.host }
