package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// Decisions in chat act only for the session shared on the connection
// (GateShared): another local process cannot decide for it without the
// connection or its reattach token.
func (h *handlers) registerReview(s *ipc.Server) {
	s.Register(ipc.MethodReviewList, ipc.Typed(h.reviewList), ipc.GateShared)
	s.Register(ipc.MethodReviewDecide, ipc.Typed(h.reviewDecide), ipc.GateShared)
	s.Register(ipc.MethodReviewCode, ipc.Typed(h.reviewCode), ipc.GateShared)
}

func (h *handlers) reviewList(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	items, err := h.p.Review.List(ctx, cs.Shared())
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []ipc.ReviewItemView{}
	}
	return ipc.ReviewListResult{Items: items}, nil
}

func (h *handlers) reviewDecide(ctx context.Context, cs *ipc.ConnState, p ipc.ReviewDecideParams) (any, error) {
	if err := required("item", p.Item); err != nil {
		return nil, err
	}
	return h.p.Review.Decide(ctx, cs.Shared(), p)
}

// reviewCode shows the item's code on this machine's desktop. The result
// never carries the code.
func (h *handlers) reviewCode(ctx context.Context, cs *ipc.ConnState, p ipc.ReviewItemParams) (any, error) {
	if err := required("item", p.Item); err != nil {
		return nil, err
	}
	return nil, h.p.Review.ShowCode(ctx, cs.Shared(), p.Item)
}
