package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerAuth(s *ipc.Server) {
	s.Register(ipc.MethodAuthUnlock, ipc.Typed(h.unlock), ipc.GateAllowWhenKilled)
}

// unlock checks the password (the Guard rate-limits and audits) and opens this
// connection's unlock window for core.UnlockTTL.
func (h *handlers) unlock(_ context.Context, cs *ipc.ConnState, p ipc.UnlockParams) (any, error) {
	if p.Password == "" {
		return nil, core.ErrBadPassword
	}
	if err := h.p.Auth.Check(p.Password); err != nil {
		return nil, err
	}
	return ipc.UnlockResult{ExpiresAt: cs.Unlock(core.UnlockTTL)}, nil
}
