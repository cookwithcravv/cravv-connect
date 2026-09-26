package api

import (
	"context"
	"log/slog"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// handlers holds what every method group needs.
type handlers struct {
	p     Ports
	clock core.Clock
}

// NewServer returns an ipc.Server with every ipc-v1 method registered, the
// kill switch wired to Control.Killed, and connection close wired to
// Sessions.Disconnect.
func NewServer(p Ports, clock core.Clock, logger *slog.Logger) *ipc.Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := ipc.NewServer(ipc.Options{
		Clock:  clock,
		Killed: p.Control.Killed,
		OnDisconnect: func(cs *ipc.ConnState) {
			if name := cs.Session(); name != "" {
				if err := p.Sessions.Disconnect(context.Background(), name); err != nil {
					logger.Warn("session disconnect", "session", name, "err", err)
				}
			}
		},
		Logger: logger,
	})
	Register(s, p, clock)
	return s
}

// Register adds every method group to s. A new group is one new file plus one
// line in this list.
func Register(s *ipc.Server, p Ports, clock core.Clock) {
	h := &handlers{p: p, clock: clock}
	for _, group := range []func(*ipc.Server){
		h.registerSession,
		h.registerAuth,
		h.registerChat,
		h.registerInbox,
		h.registerTasks,
		h.registerFiles,
		h.registerPeers,
		h.registerPairing,
		h.registerControl,
		h.registerStatus,
		h.registerHook,
	} {
		group(s)
	}
}
