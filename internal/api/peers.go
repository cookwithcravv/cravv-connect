package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerPeers(s *ipc.Server) {
	s.Register(ipc.MethodPeerList, ipc.Typed(h.peerList), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodPeerPause, ipc.Typed(h.byAlias(h.p.Peers.Pause)), ipc.GateNone)
	s.Register(ipc.MethodPeerResume, ipc.Typed(h.byAlias(h.p.Peers.Resume)), ipc.GateNone)
	s.Register(ipc.MethodPeerUnpair, ipc.Typed(h.byAlias(h.p.Peers.Unpair)), ipc.GateNone)
	s.Register(ipc.MethodPeerAlias, ipc.Typed(h.peerAlias), ipc.GateNone)
}

// peerList reuses the status snapshot so Online is computed in one place.
func (h *handlers) peerList(ctx context.Context, _ *ipc.ConnState, _ ipc.Empty) (any, error) {
	st, err := h.p.Status.Status(ctx)
	if err != nil {
		return nil, err
	}
	peers := st.Peers
	if peers == nil {
		peers = []ipc.PeerView{}
	}
	return ipc.PeerListResult{Peers: peers}, nil
}

func (h *handlers) byAlias(op func(ctx context.Context, alias string) error) func(context.Context, *ipc.ConnState, ipc.AliasParams) (any, error) {
	return func(ctx context.Context, _ *ipc.ConnState, p ipc.AliasParams) (any, error) {
		if err := required("alias", p.Alias); err != nil {
			return nil, err
		}
		return nil, op(ctx, p.Alias)
	}
}

func (h *handlers) peerAlias(ctx context.Context, _ *ipc.ConnState, p ipc.PeerAliasParams) (any, error) {
	if err := required("alias", p.Alias); err != nil {
		return nil, err
	}
	if err := validAlias(p.NewAlias); err != nil {
		return nil, err
	}
	return nil, h.p.Peers.Rename(ctx, p.Alias, p.NewAlias)
}
