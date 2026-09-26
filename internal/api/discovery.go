package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func (h *handlers) registerDiscovery(s *ipc.Server) {
	// machines is peer.list under its v2 name; both reuse the status snapshot.
	s.Register(ipc.MethodMachines, ipc.Typed(h.peerList), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodSessionsList, ipc.Typed(h.sessionsList), ipc.GateNone)
}

func (h *handlers) sessionsList(ctx context.Context, _ *ipc.ConnState, p ipc.MachineParams) (any, error) {
	if err := required("machine", p.Machine); err != nil {
		return nil, err
	}
	return h.p.Discovery.Sessions(ctx, p.Machine)
}
