package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/api"
	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/daemon"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// Run builds the daemon and the IPC server and runs both until ctx ends or one
// of them fails. The daemon fills in its own defaults: the relay client from
// Config.RelayURL, the PAM verifier from Config.PAMService, the current user,
// the desktop notifier and the identity store. version is the binary's
// version, which status reports (so a newer CLI can restart an older daemon).
func Run(ctx context.Context, paths config.Paths, logger *slog.Logger, version string) error {
	cfg, err := config.LoadReady(paths)
	if err != nil {
		return err
	}
	clock := core.SystemClock{}
	d, err := daemon.New(daemon.Options{Paths: paths, Config: cfg, Version: version, Clock: clock, Log: logger})
	if err != nil {
		return err
	}
	defer d.Close()
	ln, removePID, err := listenWithPID(paths)
	if err != nil {
		return err
	}
	defer removePID()
	return Serve(ctx, d, ln, clock, logger)
}

// listenWithPID listens on the daemon socket and only then writes the pid
// file, so a second daemon that fails to listen never overwrites the running
// daemon's pid. The returned cleanup removes the pid file only if it still
// holds this process's pid.
func listenWithPID(paths config.Paths) (net.Listener, func(), error) {
	ln, err := ipc.Listen(paths.Socket)
	if err != nil {
		return nil, nil, err
	}
	pid := strconv.Itoa(os.Getpid())
	pf := paths.PIDFile()
	if err := os.WriteFile(pf, []byte(pid), 0o600); err != nil {
		ln.Close()
		return nil, nil, fmt.Errorf("write pid file: %w", err)
	}
	return ln, func() {
		if b, err := os.ReadFile(pf); err == nil && string(b) == pid {
			os.Remove(pf)
		}
	}, nil
}

// shutdownDelay lets the daemon.shutdown reply reach the client before the
// server stops.
const shutdownDelay = 100 * time.Millisecond

// stopper implements api.LifecyclePort by cancelling Serve's context.
type stopper struct{ cancel context.CancelFunc }

func (s stopper) Shutdown() error {
	time.AfterFunc(shutdownDelay, s.cancel)
	return nil
}

// Serve runs the daemon and the ipc-v1 API on ln until ctx ends or one of
// them fails. It does not close d. Run and the e2e harness both use it, so
// tests serve the daemon exactly as `cravv-connect daemon run` does.
func Serve(ctx context.Context, d *daemon.Daemon, ln net.Listener, clock core.Clock, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ports := Ports(d)
	ports.Lifecycle = stopper{cancel}
	srv := api.NewServer(ports, clock, logger)
	// The web UI starts on ui.start and is stopped before Serve returns.
	ui := newWebUI(ctx, srv, clock, logger)
	defer ui.close()
	api.RegisterUI(srv, UIPorts(d, ui.launcher))
	api.RegisterManaged(srv, ManagedPorts(d))
	// A process inside a managed run may only bind with its run token
	// (defense in depth for shell runs; docs/security.md).
	srv.SetRunPeer(d.Host().InRun)
	errc := make(chan error, 2)
	go func() { errc <- d.Run(ctx) }()
	go func() { errc <- srv.Serve(ctx, ln) }()
	first := <-errc
	cancel()
	second := <-errc
	return errors.Join(first, second)
}
