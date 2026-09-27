package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// assembleManaged builds the offer rules and the SessionHost (they outlive
// ResetIdentity, like the shared sessions).
func (d *Daemon) assembleManaged(db store.Store, lg audit.Logger) {
	home, _ := os.UserHomeDir()
	self, err := os.Executable()
	if err != nil {
		self = "cravv-connect"
	}
	d.offers = NewOfferService(db, currentPeers{d}, FolderRules{Home: home, StateDir: d.opts.Paths.Home}, d.clock, lg)
	check := d.opts.ClaudeCheck
	if check == nil {
		check = func() error {
			if _, ok := ResolveClaude(os.Getenv, exec.LookPath, home); !ok {
				return ErrClaudeNotFound
			}
			return nil
		}
	}
	d.offers.SetClaudeCheck(check)
	d.host = NewSessionHost(HostDeps{
		Offers: d.offers, Store: db, Sessions: d.shared, Inbox: d.inbox, Links: db, Peers: db,
		Tasks:  func() HostTasks { return d.svc.Load().tasks },
		Sender: func() EnvelopeSender { return d.svc.Load().outbound },
		Adapter: func() AgentAdapter {
			return ClaudeAdapter{Path: FindClaude(os.Getenv, exec.LookPath, home)}
		},
		Runner: ExecRunner{}, RunDir: filepath.Join(d.opts.Paths.Home, "runs"), Self: self, StateDir: d.opts.Paths.Home,
		Killed: d.kill.Killed, Clock: d.clock, Audit: lg, Log: d.log,
	})
	d.offers.AddObserver(d.host)
}

// runRetention is how long run starts are kept for the caps.
const runRetention = 48 * time.Hour

// maintainManaged closes idle managed sessions and purges old run records.
func (d *Daemon) maintainManaged(ctx context.Context) []error {
	var errs []error
	if _, err := d.host.Sweep(ctx); err != nil {
		errs = append(errs, fmt.Errorf("sweep managed sessions: %w", err))
	}
	if _, err := d.store.PurgeRunsBefore(ctx, d.clock.Now().Add(-runRetention)); err != nil {
		errs = append(errs, fmt.Errorf("purge run records: %w", err))
	}
	return errs
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

// offersNow looks offers up in the daemon's offer rules, which are built
// after the identity-bound services.
type offersNow struct{ d *Daemon }

func (o offersNow) Get(ctx context.Context, id string) (store.Offer, error) {
	if o.d.offers == nil {
		return store.Offer{}, core.ErrNotFound
	}
	return o.d.offers.Get(ctx, id)
}
