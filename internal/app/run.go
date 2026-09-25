package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// Run builds the daemon and the IPC server and runs both until ctx ends or one
// of them fails. The daemon fills in its own defaults: the relay client from
// Config.RelayURL, the PAM verifier from Config.PAMService, the current user,
// the desktop notifier and the identity store.
func Run(ctx context.Context, paths config.Paths, logger *slog.Logger) error {
	cfg, err := config.Load(paths)
	if err != nil {
		return fmt.Errorf("load config (run `cravv-connect init` first): %w", err)
	}
	if cfg.RelayURL == "" {
		return errors.New("no relay_url in config.toml: run `cravv-connect init --relay <url>`")
	}
	clock := core.SystemClock{}
	d, err := daemon.New(daemon.Options{Paths: paths, Config: cfg, Clock: clock, Log: logger})
	if err != nil {
		return err
	}
	defer d.Close()
	ln, err := ipc.Listen(paths.Socket)
	if err != nil {
		return err
	}
	return Serve(ctx, d, ln, clock, logger)
}

// Serve runs the daemon and the ipc-v1 API on ln until ctx ends or one of
// them fails. It does not close d. Run and the e2e harness both use it, so
// tests serve the daemon exactly as `cravv-connect daemon run` does.
func Serve(ctx context.Context, d *daemon.Daemon, ln net.Listener, clock core.Clock, logger *slog.Logger) error {
	srv := api.NewServer(Ports(d), clock, logger)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- d.Run(ctx) }()
	go func() { errc <- srv.Serve(ctx, ln) }()
	first := <-errc
	cancel()
	second := <-errc
	return errors.Join(first, second)
}
