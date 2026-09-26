// Package webui is the local web UI: an HTTP server on 127.0.0.1 that the
// daemon starts on request (ui.start). It is a thin client over the IPC API.
// Every browser session holds its own in-process IPC connection, so the
// daemon's gates, tiers and password lockout apply unchanged, and a password
// unlock belongs to that browser session only.
package webui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Limits of the UI server.
const (
	// IdleTimeout stops the server this long after the last request.
	IdleTimeout = 30 * time.Minute
	// LaunchTokenTTL is how long a launch URL works (once).
	LaunchTokenTTL = 2 * time.Minute
	// MaxSessions caps browser sessions; the oldest is ended first.
	MaxSessions = 8
	// MaxLaunchTokens caps unused launch tokens; the oldest is dropped first.
	MaxLaunchTokens = 8
	// DefaultSweepEvery is how often the idle timeout is checked.
	DefaultSweepEvery = time.Minute
)

// ErrStopping is returned by Start once the daemon is shutting down.
var ErrStopping = errors.New("the daemon is stopping")

// Caller is one IPC connection to the daemon (satisfied by *ipc.Client).
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
	Close() error
}

// Dialer opens a new daemon connection. Each browser session gets its own.
type Dialer func() (Caller, error)

// Options configures a Launcher.
type Options struct {
	Clock core.Clock
	Dial  Dialer
	// Pages are the pages and actions to serve; nil serves DefaultRegistry().
	Pages  *Registry
	Logger *slog.Logger
	// SweepEvery is how often the idle timeout is checked (default
	// DefaultSweepEvery).
	SweepEvery time.Duration
	// Listen opens the listener (default: TCP on 127.0.0.1, random port).
	Listen func() (net.Listener, error)
}

// Launcher starts the UI server on demand and stops it when it has been idle
// for IdleTimeout or when the daemon's context ends. It implements
// api.UIPort.
type Launcher struct {
	o       Options
	mu      sync.Mutex
	stopped bool
	run     *running
}

// running is one server instance; a restart after an idle stop is a new one
// with a new port, new tokens and no sessions.
type running struct {
	srv  *server
	http *http.Server
	port int
	stop chan struct{}
	done chan struct{}
}

// NewLauncher returns a Launcher that stops for good when ctx ends.
func NewLauncher(ctx context.Context, o Options) *Launcher {
	if o.Clock == nil {
		o.Clock = core.SystemClock{}
	}
	if o.Pages == nil {
		o.Pages = DefaultRegistry()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = DefaultSweepEvery
	}
	if o.Listen == nil {
		o.Listen = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	}
	l := &Launcher{o: o}
	context.AfterFunc(ctx, l.Stop)
	return l
}

// Start starts the server if it is not running and returns a launch URL
// with a new one-time token.
func (l *Launcher) Start(context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return "", ErrStopping
	}
	if l.run == nil {
		r, err := l.startLocked()
		if err != nil {
			return "", err
		}
		l.run = r
	}
	tok, err := l.run.srv.mintToken()
	if err != nil {
		return "", err
	}
	l.run.srv.touch()
	return fmt.Sprintf("http://127.0.0.1:%d/launch?token=%s", l.run.port, tok), nil
}

func (l *Launcher) startLocked() (*running, error) {
	ln, err := l.o.Listen()
	if err != nil {
		return nil, fmt.Errorf("start the web UI: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv, err := newServer(l.o, port)
	if err != nil {
		ln.Close()
		return nil, err
	}
	r := &running{
		srv: srv, port: port, stop: make(chan struct{}), done: make(chan struct{}),
		http: &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second},
	}
	go func() {
		defer close(r.done)
		if err := r.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.o.Logger.Warn("web UI stopped", "err", err)
		}
	}()
	go l.sweepLoop(r)
	l.o.Logger.Info("web UI started", "port", port)
	return r, nil
}

func (l *Launcher) sweepLoop(r *running) {
	t := time.NewTicker(l.o.SweepEvery)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			l.sweep()
		}
	}
}

// sweep stops the server once IdleTimeout has passed since the last request.
func (l *Launcher) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run != nil && l.o.Clock.Now().Sub(l.run.srv.lastRequest()) >= IdleTimeout {
		l.o.Logger.Info("web UI idle, stopping")
		l.stopRunLocked()
	}
}

func (l *Launcher) stopRunLocked() {
	r := l.run
	l.run = nil
	close(r.stop)
	r.http.Close()
	<-r.done
	r.srv.closeSessions()
}

// Running reports whether the server is up.
func (l *Launcher) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.run != nil
}

// Stop stops the server and refuses later starts. The daemon's context
// ending calls it.
func (l *Launcher) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	if l.run != nil {
		l.stopRunLocked()
	}
}
