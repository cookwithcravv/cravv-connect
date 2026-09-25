// Command cravv-relay runs the reference Go relay with the in-memory backend.
// It is for development and tests: all state is lost on exit.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/relayserver"
)

type options struct {
	addr       string
	origin     string
	adminToken string
}

// parseOptions reads flags, falling back to CRAVV_RELAY_ADMIN_TOKEN for the admin token.
func parseOptions(args []string, getenv func(string) string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("cravv-relay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.addr, "addr", "127.0.0.1:8787", "listen address")
	fs.StringVar(&o.origin, "origin", "", "public origin clients dial, scheme://host[:port] (default http://<addr>)")
	fs.StringVar(&o.adminToken, "admin-token", "", "admin token for the first registration (or env CRAVV_RELAY_ADMIN_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if o.adminToken == "" {
		o.adminToken = getenv("CRAVV_RELAY_ADMIN_TOKEN")
	}
	if o.adminToken == "" {
		return options{}, errors.New("an admin token is required: pass -admin-token or set CRAVV_RELAY_ADMIN_TOKEN")
	}
	if o.origin == "" {
		host, port, err := net.SplitHostPort(o.addr)
		if err != nil {
			return options{}, fmt.Errorf("-addr %q: %w", o.addr, err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		o.origin = "http://" + net.JoinHostPort(host, port)
	}
	origin, err := relayproto.NormalizeOrigin(o.origin)
	if err != nil {
		return options{}, fmt.Errorf("-origin: %w", err)
	}
	o.origin = origin
	return o, nil
}

func main() {
	o, err := parseOptions(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cravv-relay:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	clock := core.SystemClock{}
	srv, err := relayserver.New(relayserver.Config{
		PublicOrigin: o.origin,
		AdminToken:   o.adminToken,
		Clock:        clock,
		Logger:       log,
	}, relayserver.NewMemoryBackend(clock))
	if err != nil {
		fmt.Fprintln(os.Stderr, "cravv-relay:", err)
		os.Exit(2)
	}
	defer srv.Close()

	hs := &http.Server{Addr: o.addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("cravv-relay listening", "addr", o.addr, "origin", o.origin, "backend", "memory")
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
