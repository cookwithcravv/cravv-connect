package daemon

import (
	"context"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// OfferLister lists the offers made to a paired machine. Implemented by *OfferService.
type OfferLister interface {
	ForPeer(ctx context.Context, peer core.MachineID) ([]store.Offer, error)
}

// SetOffers makes sessions.list answers carry the offers made to the asker
// (labels only: never the folder or the limits). Call it before the daemon runs.
func (d *Discovery) SetOffers(o OfferLister) { d.offers = o }

// listedOffers returns the offers made to peer, as sent in sessions.listed.
func (d *Discovery) listedOffers(ctx context.Context, peer core.MachineID) ([]core.ListedOffer, error) {
	out := []core.ListedOffer{}
	if d.offers == nil {
		return out, nil
	}
	list, err := d.offers.ForPeer(ctx, peer)
	if err != nil {
		return nil, err
	}
	for _, o := range list {
		out = append(out, core.ListedOffer{OfferID: o.ID, Label: o.Label, Agent: o.Agent, MaxPermission: o.Permission})
	}
	return out, nil
}

// cleanListedOffer validates one offer in a peer's sessions.listed.
func cleanListedOffer(o core.ListedOffer) (core.ListedOffer, bool) {
	if !core.ValidID(o.OfferID) || !core.ValidOfferLabel(o.Label) || !o.MaxPermission.Valid() {
		return core.ListedOffer{}, false
	}
	if !validAgentLabel(o.Agent) {
		o.Agent = ""
	}
	return o, true
}
