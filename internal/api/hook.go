package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerHook(s *ipc.Server) {
	s.Register(ipc.MethodHookCounts, ipc.Typed(h.hookCounts), ipc.GateAllowWhenKilled)
}

// hookCounts answers a hook run with counts and local names only; it never
// returns bodies or peer-chosen names.
func (h *handlers) hookCounts(ctx context.Context, _ *ipc.ConnState, p ipc.HookCountsParams) (any, error) {
	return h.p.Hook.Check(ctx, p)
}
