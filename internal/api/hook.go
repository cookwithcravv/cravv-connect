package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
)

func (h *handlers) registerHook(s *ipc.Server) {
	s.Register(ipc.MethodHookCounts, ipc.Typed(h.hookCounts), ipc.GateAllowWhenKilled)
}

// hookCounts returns counts keyed by local alias only; it never returns bodies
// or peer-chosen names.
func (h *handlers) hookCounts(ctx context.Context, _ *ipc.ConnState, p ipc.HookCountsParams) (any, error) {
	unread, approvals, err := h.p.Hook.Counts(ctx, p.Cwd)
	if err != nil {
		return nil, err
	}
	total := 0
	for _, n := range unread {
		total += n
	}
	return ipc.HookCountsResult{Notice: present.Notice(unread, approvals), Unread: total, Approvals: approvals}, nil
}
