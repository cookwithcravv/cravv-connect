package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerChat(s *ipc.Server) {
	s.Register(ipc.MethodChatSend, ipc.Typed(h.chatSend), ipc.GateSession)
}

func (h *handlers) chatSend(ctx context.Context, cs *ipc.ConnState, p ipc.ChatSendParams) (any, error) {
	if err := required("to", p.To); err != nil {
		return nil, err
	}
	if err := required("text", p.Text); err != nil {
		return nil, err
	}
	if len(p.Text) > core.MaxTextBytes {
		return nil, core.ErrTooLarge
	}
	id, err := h.p.Chat.Send(ctx, cs.Session(), p.To, p.Text)
	if err != nil {
		return nil, err
	}
	return ipc.IDResult{ID: id}, nil
}
