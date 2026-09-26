package daemon

import (
	"context"
	"errors"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// PermissionPolicy maps (a link's permission_in, kind) to a decision (v2
// spec 3.4). Only task.create depends on the level; every other link-scoped
// kind is delivered on an active link.
type PermissionPolicy struct{}

// permissionRules holds the kinds whose decision depends on the level.
var permissionRules = map[core.Kind]map[core.Permission]Decision{
	core.KindTaskCreate: {
		core.PermMessages:  DecisionReject,
		core.PermTasksAsk:  DecisionHold,
		core.PermTasksAuto: DecisionDeliver,
	},
}

// Decide returns the decision. An invalid level rejects everything, so a
// corrupt record never widens access.
func (PermissionPolicy) Decide(perm core.Permission, kind core.Kind) Decision {
	if !perm.Valid() {
		return DecisionReject
	}
	if rules, gated := permissionRules[kind]; gated {
		return rules[perm]
	}
	return DecisionDeliver
}

// LinkLookup finds a link by its key. Implemented by store.LinkStore.
type LinkLookup interface {
	GetLink(ctx context.Context, peer core.MachineID, id string) (store.Link, error)
}

// SessionLookup returns a shared session. Implemented by *SessionService.
type SessionLookup interface {
	Get(ctx context.Context, id string) (store.SharedSession, error)
}

// GateReplier answers traffic the gate drops. Implemented by *LinkReplies.
type GateReplier interface {
	UnknownLinkReplier
	Unsupported(ctx context.Context, peer store.Peer)
}

type linkKeyCtx struct{}

// withLink records the link the gate admitted an envelope on.
func withLink(ctx context.Context, l store.Link) context.Context {
	return context.WithValue(ctx, linkKeyCtx{}, l)
}

// LinkFrom returns the link a LinkGate admitted this envelope on. Handlers
// take the sender's session and the local session from it, never from the
// envelope.
func LinkFrom(ctx context.Context) (store.Link, bool) {
	l, ok := ctx.Value(linkKeyCtx{}).(store.Link)
	return l, ok
}

// errNoLink is returned by a link-scoped handler invoked without a LinkGate
// in front: it never acts on an envelope the gate did not admit.
var errNoLink = errors.New("link-scoped kind handled without a link gate")

// LinkGate is the single enforcement point for chat, task.* and file.offer
// (v2 spec section 10). An envelope reaches Inner only when:
//   - it carries a link_id (else control.unsupported, rate-limited, and dropped);
//   - the link (keyed by the sender machine and link_id) is active here and
//     its local session is open or away (else link.closed{unknown_link},
//     rate-limited, and dropped);
//   - the link's permission_in allows the kind (else OnReject, or dropped).
//
// Inner gets the link (LinkFrom) and the decision (DecisionFrom) in its context.
type LinkGate struct {
	Links    LinkLookup
	Sessions SessionLookup
	Replies  GateReplier
	Policy   PermissionPolicy
	Inner    Handler
	OnReject Handler
}

// Handle implements Handler.
func (g LinkGate) Handle(ctx context.Context, peer store.Peer, env core.Envelope) error {
	if env.LinkID == "" {
		g.Replies.Unsupported(ctx, peer)
		return nil
	}
	l, err := g.Links.GetLink(ctx, peer.MachineID, env.LinkID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return Retryable(err)
	}
	if err != nil || l.State != store.LinkActive {
		g.Replies.UnknownLink(ctx, peer, env.LinkID)
		return nil
	}
	sess, err := g.Sessions.Get(ctx, l.Session)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return Retryable(err)
	}
	if err != nil || sess.State == core.SessionClosed {
		g.Replies.UnknownLink(ctx, peer, env.LinkID)
		return nil
	}
	d := g.Policy.Decide(l.PermissionIn, env.Kind)
	ctx = withLink(withDecision(ctx, d), l)
	if d == DecisionReject {
		if g.OnReject == nil {
			return nil
		}
		return g.OnReject.Handle(ctx, peer, env)
	}
	return g.Inner.Handle(ctx, peer, env)
}
