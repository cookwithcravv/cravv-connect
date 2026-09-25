package api

import (
	"context"
	"path/filepath"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerControl(s *ipc.Server) {
	s.Register(ipc.MethodKill, ipc.Typed(h.kill), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodResume, ipc.Typed(h.resume), ipc.GateUnlock|ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodAllowPathAdd, ipc.Typed(h.allowPath), ipc.GateUnlock)
	s.Register(ipc.MethodResetIdentity, ipc.Typed(h.resetIdentity), ipc.GateUnlock)
}

func (h *handlers) kill(ctx context.Context, _ *ipc.ConnState, _ ipc.Empty) (any, error) {
	return nil, h.p.Control.Kill(ctx)
}

func (h *handlers) resume(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	return nil, h.p.Control.Resume(ctx, cs.Unlocked())
}

func (h *handlers) allowPath(ctx context.Context, cs *ipc.ConnState, p ipc.AllowPathParams) (any, error) {
	if err := absPath("path", p.Path); err != nil {
		return nil, err
	}
	return nil, h.p.Control.AddAllowPath(ctx, filepath.Clean(p.Path), cs.Unlocked())
}

func (h *handlers) resetIdentity(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	return nil, h.p.Control.ResetIdentity(ctx, cs.Unlocked())
}
