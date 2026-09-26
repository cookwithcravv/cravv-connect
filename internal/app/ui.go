package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/webui"
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

// webUI is the daemon's web UI: a webui.Launcher whose browser sessions are
// in-process pipes into the IPC server, so every UI action passes the same
// gates as the CLI.
type webUI struct {
	launcher *webui.Launcher
	srv      *ipc.Server
	ctx      context.Context
	mu       sync.Mutex
	closed   bool
	pipes    sync.WaitGroup
}

// newWebUI builds the UI for srv. It stops when ctx ends.
func newWebUI(ctx context.Context, srv *ipc.Server, clock core.Clock, logger *slog.Logger) *webUI {
	w := &webUI{srv: srv, ctx: ctx}
	w.launcher = webui.NewLauncher(ctx, webui.Options{Clock: clock, Dial: w.dial, Logger: logger})
	return w
}

// dial opens a browser session's connection to the IPC server.
func (w *webUI) dial() (webui.Caller, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, webui.ErrStopping
	}
	c, done := w.srv.Pipe(w.ctx)
	w.pipes.Add(1)
	go func() {
		<-done
		w.pipes.Done()
	}()
	return c, nil
}

// close stops the UI server and waits until no browser session's
// connection is being served, so the daemon can be closed after it.
func (w *webUI) close() {
	w.launcher.Stop()
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.pipes.Wait()
}
