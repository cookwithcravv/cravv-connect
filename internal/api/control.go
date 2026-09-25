package api

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerControl(s *ipc.Server) {
	s.Register(ipc.MethodKill, ipc.Typed(h.kill), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodResume, ipc.Typed(h.resume), ipc.GateUnlock|ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodAllowPathAdd, ipc.Typed(h.allowPath), ipc.GateUnlock)
	s.Register(ipc.MethodResetIdentity, ipc.Typed(h.resetIdentity), ipc.GateUnlock)
	// Stopping the daemon cuts traffic off, like kill: agents may do it, no
	// password, also while killed. Starting it again needs no password either.
	s.Register(ipc.MethodDaemonShutdown, ipc.Typed(h.shutdown), ipc.GateAllowWhenKilled)
}

func (h *handlers) shutdown(context.Context, *ipc.ConnState, ipc.Empty) (any, error) {
	if h.p.Lifecycle == nil {
		return nil, fmt.Errorf("%w: this daemon cannot shut itself down", ipc.ErrBadRequest)
	}
	return nil, h.p.Lifecycle.Shutdown()
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
