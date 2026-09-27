package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// ManagedStarter creates a managed session for a link request to an offer,
// and closes one whose link could not be stored. Implemented by *SessionHost.
type ManagedStarter interface {
	StartManaged(ctx context.Context, peer store.Peer, offerID, linkID string) (store.SharedSession, core.Permission, error)
	Close(ctx context.Context, sessionID, reason string) error
}

// offerTarget is how a pending link to an offer names its remote side
// until the peer answers with the managed session it created.
const offerTarget = "new:"

// connectOffer asks machine to start a managed session from its offer
// called label. The link is pending until the peer answers; the peer
// creates the session and accepts at once when its rules allow.
func (s *LinkService) connectOffer(ctx context.Context, sess store.SharedSession, machine, label string, proposed core.Permission, note string) (store.Link, error) {
	peer, listed, err := s.d.Directory.List(ctx, machine)
	if err != nil {
		return store.Link{}, err
	}
	var offer *core.ListedOffer
	for i := range listed.Offers {
		if listed.Offers[i].Label == label {
			offer = &listed.Offers[i]
			break
		}
	}
	if offer == nil {
		return store.Link{}, fmt.Errorf("offer %s on %s: %w", label, peer.Alias, core.ErrNotFound)
	}
	now := s.d.Clock.Now()
	l, err := s.d.Links.InsertLink(ctx, store.Link{
		Peer: peer.MachineID, ID: core.NewIDAt(s.d.Clock), Direction: store.LinkOutbound, Session: sess.ID,
		RemoteName: offerTarget + offer.Label, PermissionIn: core.PermMessages, Proposed: proposed, State: store.LinkPending,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(core.LinkRequestExpiry),
	})
	if err != nil {
		return store.Link{}, err
	}
	body := core.LinkRequestBody{
		LinkID: l.ID, FromSession: core.SessionRef{ID: sess.ID, Name: sess.Name, Purpose: sess.Purpose},
		OfferID: offer.OfferID, ProposedPermission: proposed, Note: note,
	}
	if _, err := s.d.Sender.SendEnvelope(ctx, peer.MachineID, core.KindLinkRequest, "", body); err != nil {
		_, _ = s.d.Links.UpdateLink(ctx, l.Peer, l.ID, func(x *store.Link) error {
			x.State, x.Reason, x.ExpiresAt, x.UpdatedAt = store.LinkClosed, "not sent: "+err.Error(), time.Time{}, now
			return nil
		})
		return store.Link{}, err
	}
	s.record(audit.EvLinkRequest, peer, l, map[string]any{"direction": "out", "proposed": string(proposed), "offer": offer.Label})
	return l, nil
}

// managedGrant is what a link to a managed session gets: the lower of the
// proposal and the offer, except that tasks-ask becomes messages, because
// a managed session has no human to ask.
func managedGrant(proposed, offer core.Permission) core.Permission {
	grant := core.MinPermission(proposed, offer)
	if grant == core.PermTasksAsk {
		return core.PermMessages
	}
	return grant
}

// acceptOffer answers a link request to an offer (v2 spec 6.2): the host
// checks the rules, the caps and the concurrency limit and creates the
// managed session, and the link is accepted at once at the lower of the
// proposed level and the offer's permission (tasks-ask becomes messages). The password-gated rule was
// the human's approval, so nobody is asked.
func (s *LinkService) acceptOffer(ctx context.Context, peer store.Peer, b core.LinkRequestBody, from core.SessionRef) error {
	if s.d.Managed == nil {
		return s.reject(ctx, peer, b.LinkID, core.RejectNotFound)
	}
	sess, perm, err := s.d.Managed.StartManaged(ctx, peer, b.OfferID, b.LinkID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return s.reject(ctx, peer, b.LinkID, core.RejectNotFound)
	case errors.Is(err, ErrManagedBusy):
		return s.reject(ctx, peer, b.LinkID, core.RejectBusy)
	case errors.Is(err, ErrBadFolder):
		s.d.Log.Warn("managed session refused: its folder no longer passes the checks", "peer", peer.Alias, "err", err)
		return s.reject(ctx, peer, b.LinkID, core.RejectPolicy)
	case err != nil:
		return Retryable(err)
	}
	grant := managedGrant(b.ProposedPermission, perm)
	now := s.d.Clock.Now()
	l, err := s.d.Links.InsertLink(ctx, store.Link{
		Peer: peer.MachineID, ID: b.LinkID, Direction: store.LinkInbound, Session: sess.ID,
		RemoteSession: from.ID, RemoteName: from.Name, RemotePurpose: from.Purpose,
		PermissionIn: grant, PermissionOut: core.PermMessages, Proposed: b.ProposedPermission, Note: b.Note,
		State: store.LinkActive, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		if cerr := s.d.Managed.Close(ctx, sess.ID, "link not stored"); cerr != nil {
			s.d.Log.Warn("close unlinked managed session", "err", cerr)
		}
		if errors.Is(err, store.ErrLinkExists) {
			return nil
		}
		return Retryable(err)
	}
	body := core.LinkAcceptedBody{
		LinkID: l.ID, ToSession: core.SessionRef{ID: sess.ID, Name: sess.Name, Purpose: sess.Purpose}, GrantedPermission: grant,
	}
	s.record(audit.EvLinkRequest, peer, l, map[string]any{"direction": "in", "proposed": string(l.Proposed), "offer": b.OfferID})
	if _, err := s.d.Sender.SendEnvelope(ctx, peer.MachineID, core.KindLinkAccepted, "", body); err != nil {
		return err
	}
	s.record(audit.EvLinkAccept, peer, l, map[string]any{"permission": string(grant), "authority": "offer"})
	return nil
}
