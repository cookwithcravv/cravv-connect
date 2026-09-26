package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerPairing(s *ipc.Server) {
	s.Register(ipc.MethodPairStart, ipc.Typed(h.pairStart), ipc.GateUnlock)
	s.Register(ipc.MethodPairAwait, ipc.Typed(h.pairAwait), ipc.GateUnlock)
	s.Register(ipc.MethodJoinStart, ipc.Typed(h.joinStart), ipc.GateUnlock)
	s.Register(ipc.MethodPairFinalize, ipc.Typed(h.pairFinalize), ipc.GateUnlock)
}

func (h *handlers) pairStart(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	return h.p.Pairing.Start(ctx, cs.Unlocked())
}

func (h *handlers) pairAwait(ctx context.Context, _ *ipc.ConnState, p ipc.PairAwaitParams) (any, error) {
	if err := required("pending_id", p.PendingID); err != nil {
		return nil, err
	}
	return h.p.Pairing.Await(ctx, p.PendingID)
}

func (h *handlers) joinStart(ctx context.Context, cs *ipc.ConnState, p ipc.JoinStartParams) (any, error) {
	if err := required("code", p.Code); err != nil {
		return nil, err
	}
	return h.p.Pairing.Join(ctx, p.Code, cs.Unlocked())
}

func (h *handlers) pairFinalize(ctx context.Context, cs *ipc.ConnState, p ipc.PairFinalizeParams) (any, error) {
	if err := required("pending_id", p.PendingID); err != nil {
		return nil, err
	}
	if err := validAlias(p.Alias); err != nil {
		return nil, err
	}
	alias, err := h.p.Pairing.Finalize(ctx, p.PendingID, p.Alias, cs.Unlocked())
	if err != nil {
		return nil, err
	}
	return ipc.PairFinalizeResult{Alias: alias}, nil
}
