package api

import (
	"context"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerSession(s *ipc.Server) {
	s.Register(ipc.MethodSessionRegister, ipc.Typed(h.sessionRegister), ipc.GateNone)
}

func (h *handlers) sessionRegister(ctx context.Context, cs *ipc.ConnState, p ipc.SessionRegisterParams) (any, error) {
	if cs.Session() != "" {
		return nil, badRequest("a session is already registered on this connection")
	}
	if err := absPath("project_dir", p.ProjectDir); err != nil {
		return nil, err
	}
	agent := strings.TrimSpace(p.Agent)
	if agent == "" {
		agent = "agent"
	}
	name, err := h.p.Sessions.Register(ctx, agent, p.ProjectDir)
	if err != nil {
		return nil, err
	}
	cs.SetSession(name, p.ProjectDir)
	cs.SetAgent(agent)
	return ipc.SessionRegisterResult{Name: name}, nil
}
