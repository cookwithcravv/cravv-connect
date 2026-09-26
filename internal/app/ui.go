package app

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// UIPorts adapts the daemon to the web UI methods. ui starts the web server
// (Serve passes the webui.Launcher); link requests reuse the links adapter.
func UIPorts(d *daemon.Daemon, ui api.UIPort) api.UIPorts {
	return api.UIPorts{UI: ui, Local: localSessions{d}, Links: links{d}}
}

// localSessions adapts the daemon's SessionService to api.LocalSessionPort.
type localSessions struct{ d *daemon.Daemon }

func (a localSessions) Local(ctx context.Context) ([]ipc.SharedSessionView, error) {
	list, err := a.d.Shared().List(ctx, core.SessionOpen, core.SessionAway)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.SharedSessionView, 0, len(list))
	for _, s := range list {
		out = append(out, shared{a.d}.view(ctx, s))
	}
	return out, nil
}

func (a localSessions) OpenByName(ctx context.Context, name string) (string, error) {
	list, err := a.d.Shared().List(ctx, core.SessionOpen)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("no open session %q on this machine: %w", name, core.ErrNotFound)
}
