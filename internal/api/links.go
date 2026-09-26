package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerLinks(s *ipc.Server) {
	s.Register(ipc.MethodLinkConnect, ipc.Typed(h.linkConnect), ipc.GateShared)
	s.Register(ipc.MethodLinks, ipc.Typed(h.links), ipc.GateNone)
	// Cut-offs need no password, like pause and kill.
	s.Register(ipc.MethodLinkDisconnect, ipc.Typed(h.linkDisconnect), ipc.GateNone)
	s.Register(ipc.MethodLinkRestrict, ipc.Typed(h.linkRestrict), ipc.GateNone)
	s.Register(ipc.MethodLinkPermit, ipc.Typed(h.linkPermit), ipc.GateUnlock)
	// Rejecting needs nothing; accepting needs the password (checked by the daemon).
	s.Register(ipc.MethodLinkDecide, ipc.Typed(h.linkDecide), ipc.GateNone)
}

// scope is the shared session a connection acts for: its own session when it
// shared one, "" (every link) for a human connection with no registered agent
// session. An agent connection that has not shared a session, or has closed
// it, acts for none and gets core.ErrNotShared. A connection whose session was
// taken over by a reattach is scoped to nothing it can reach.
func (h *handlers) scope(ctx context.Context, cs *ipc.ConnState) (string, error) {
	id := cs.Shared()
	if id == "" {
		if cs.Session() != "" {
			return "", core.ErrNotShared
		}
		return "", nil
	}
	if err := h.p.Shared.Current(ctx, id, cs.ID()); err != nil {
		return "", err
	}
	return id, nil
}

func (h *handlers) linkConnect(ctx context.Context, cs *ipc.ConnState, p ipc.LinkConnectParams) (any, error) {
	if err := required("target", p.Target); err != nil {
		return nil, err
	}
	if err := required("permission", p.Permission); err != nil {
		return nil, err
	}
	return h.p.Links.Connect(ctx, cs.Shared(), p.Target, p.Permission, p.Note)
}

func (h *handlers) links(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	scope, err := h.scope(ctx, cs)
	if err != nil {
		return nil, err
	}
	ls, err := h.p.Links.List(ctx, scope)
	if err != nil {
		return nil, err
	}
	if ls == nil {
		ls = []ipc.LinkView{}
	}
	return ipc.LinksResult{Links: ls}, nil
}

func linkNumber(n int64) error {
	if n <= 0 {
		return badRequest("link is required")
	}
	return nil
}

func (h *handlers) linkDisconnect(ctx context.Context, cs *ipc.ConnState, p ipc.LinkParams) (any, error) {
	if err := linkNumber(p.Link); err != nil {
		return nil, err
	}
	scope, err := h.scope(ctx, cs)
	if err != nil {
		return nil, err
	}
	return nil, h.p.Links.Disconnect(ctx, scope, p.Link)
}

func (h *handlers) linkRestrict(ctx context.Context, cs *ipc.ConnState, p ipc.LinkPermissionParams) (any, error) {
	if err := linkNumber(p.Link); err != nil {
		return nil, err
	}
	if err := required("permission", p.Permission); err != nil {
		return nil, err
	}
	scope, err := h.scope(ctx, cs)
	if err != nil {
		return nil, err
	}
	return h.p.Links.Restrict(ctx, scope, p.Link, p.Permission)
}

func (h *handlers) linkPermit(ctx context.Context, cs *ipc.ConnState, p ipc.LinkPermissionParams) (any, error) {
	if err := linkNumber(p.Link); err != nil {
		return nil, err
	}
	if err := required("permission", p.Permission); err != nil {
		return nil, err
	}
	return h.p.Links.Permit(ctx, p.Link, p.Permission, cs.Unlocked())
}

func (h *handlers) linkDecide(ctx context.Context, cs *ipc.ConnState, p ipc.LinkDecideParams) (any, error) {
	if err := linkNumber(p.Link); err != nil {
		return nil, err
	}
	return h.p.Links.Decide(ctx, p.Link, p.Accept, p.Permission, cs.Unlocked())
}
