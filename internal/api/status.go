package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// Audit read limits.
const (
	DefaultAuditLimit = 50
	MaxAuditLimit     = 1000
)

func (h *handlers) registerStatus(s *ipc.Server) {
	s.Register(ipc.MethodStatus, ipc.Typed(h.status), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodAuditRead, ipc.Typed(h.auditRead), ipc.GateAllowWhenKilled)
}

func (h *handlers) status(ctx context.Context, _ *ipc.ConnState, _ ipc.Empty) (any, error) {
	return h.p.Status.Status(ctx)
}

func (h *handlers) auditRead(ctx context.Context, _ *ipc.ConnState, p ipc.AuditReadParams) (any, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultAuditLimit
	}
	events, err := h.p.Audit.Read(ctx, min(limit, MaxAuditLimit))
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []audit.Event{}
	}
	return ipc.AuditReadResult{Events: events}, nil
}
