package api

import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// Inbox paging limits.
const (
	DefaultInboxLimit = 50
	MaxInboxLimit     = 200
)

func (h *handlers) registerInbox(s *ipc.Server) {
	s.Register(ipc.MethodInboxCheck, ipc.Typed(h.inboxCheck), ipc.GateShared)
	s.Register(ipc.MethodInboxWait, ipc.Typed(h.inboxWait), ipc.GateShared)
}

func (h *handlers) inboxCheck(ctx context.Context, cs *ipc.ConnState, p ipc.InboxCheckParams) (any, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultInboxLimit
	}
	limit = min(limit, MaxInboxLimit)
	items, err := h.p.Inbox.Check(ctx, cs.Shared(), limit)
	if err != nil {
		return nil, err
	}
	return inboxResult(items), nil
}

// WaitTimeout clamps a requested wait to (0, core.MaxWait]; 0 or less means
// the maximum.
func WaitTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return core.MaxWait
	}
	return min(time.Duration(seconds)*time.Second, core.MaxWait)
}

func (h *handlers) inboxWait(ctx context.Context, cs *ipc.ConnState, p ipc.InboxWaitParams) (any, error) {
	items, err := h.p.Inbox.Wait(ctx, cs.Shared(), WaitTimeout(p.TimeoutS))
	if err != nil {
		return nil, err
	}
	return inboxResult(items), nil
}

// inboxResult never returns a null items list.
func inboxResult(items []ipc.InboxView) ipc.InboxResult {
	if items == nil {
		items = []ipc.InboxView{}
	}
	return ipc.InboxResult{Items: items}
}
