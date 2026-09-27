package api

import (
	"context"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerShared(s *ipc.Server) {
	s.Register(ipc.MethodSessionShare, ipc.Typed(h.sessionShare), ipc.GateSession)
	s.Register(ipc.MethodSessionClose, ipc.Typed(h.sessionClose), ipc.GateShared)
	s.Register(ipc.MethodSessionSet, ipc.Typed(h.sessionSet), ipc.GateShared)
	s.Register(ipc.MethodSessionReattach, ipc.Typed(h.sessionReattach), ipc.GateSession)
	s.Register(ipc.MethodSessionListen, ipc.Typed(h.sessionListen), ipc.GateNone)
}

// sessionShare shares the chat on this connection. The agent and project
// folder come from the connection's registration, never from the call.
func (h *handlers) sessionShare(ctx context.Context, cs *ipc.ConnState, p ipc.SessionShareParams) (any, error) {
	if cs.Shared() != "" {
		if err := h.p.Shared.Current(ctx, cs.Shared(), cs.ID()); err == nil {
			return nil, badRequest("this chat already shares a session")
		}
	}
	if err := required("name", p.Name); err != nil {
		return nil, err
	}
	id, res, err := h.p.Shared.Share(ctx, cs.ID(), cs.Agent(), cs.ProjectDir(), p.Name, p.Purpose, p.Visibility)
	if err != nil {
		return nil, err
	}
	cs.SetShared(id)
	if p.AgentSession != "" {
		h.p.Hook.Bind(ctx, p.AgentSession, id)
	}
	return res, nil
}

func (h *handlers) sessionClose(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	if err := h.p.Shared.Close(ctx, cs.Shared()); err != nil {
		return nil, err
	}
	cs.SetShared("")
	return nil, nil
}

func (h *handlers) sessionSet(ctx context.Context, cs *ipc.ConnState, p ipc.SessionSetParams) (any, error) {
	if p.Purpose == nil && p.Visibility == nil {
		return nil, badRequest("nothing to change: give purpose or visibility")
	}
	return h.p.Shared.Set(ctx, cs.Shared(), p.Purpose, p.Visibility)
}

// sessionReattach takes a shared session over with its reattach token. The
// request must come from the same agent and project folder that shared it.
func (h *handlers) sessionReattach(ctx context.Context, cs *ipc.ConnState, p ipc.SessionReattachParams) (any, error) {
	if err := required("reattach_token", p.ReattachToken); err != nil {
		return nil, err
	}
	id, view, err := h.p.Shared.Reattach(ctx, cs.ID(), p.ReattachToken, cs.Agent(), cs.ProjectDir())
	if err != nil {
		return nil, err
	}
	cs.SetShared(id)
	if p.AgentSession != "" {
		h.p.Hook.Bind(ctx, p.AgentSession, id)
	}
	return view, nil
}

// MaxListenTimeout caps a timed session.listen.
const MaxListenTimeout = 24 * time.Hour

func (h *handlers) sessionListen(ctx context.Context, _ *ipc.ConnState, p ipc.SessionListenParams) (any, error) {
	if err := required("wake_token", p.WakeToken); err != nil {
		return nil, err
	}
	timeout := time.Duration(max(p.TimeoutS, 0)) * time.Second
	return h.p.Shared.Listen(ctx, p.WakeToken, min(timeout, MaxListenTimeout))
}
