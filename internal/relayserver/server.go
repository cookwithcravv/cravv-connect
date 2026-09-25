// Package relayserver is the reference relay-v1 implementation in Go. It backs the
// test suites and cmd/cravv-relay; storage is pluggable through Backend.
package relayserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
)

// Config configures a Server.
type Config struct {
	// PublicOrigin is scheme://host[:port] exactly as clients compute it from the URL
	// they dial. It is bound into every auth signature, so it must be configured,
	// never derived from request headers.
	PublicOrigin string
	AdminToken   string
	Clock        core.Clock
	Limits       Limits
	Logger       *slog.Logger // nil discards logs
}

// Server serves relay-v1 over HTTP and WebSocket.
type Server struct {
	cfg       Config
	be        Backend
	mux       *http.ServeMux
	hub       *hub
	limiter   *rateLimiter // per mailbox
	ipLimiter *rateLimiter // per client IP
	log       *slog.Logger
	ops       map[string]opHandler
}

// New builds a Server. Zero Limits fields take DefaultLimits values.
func New(cfg Config, be Backend) *Server {
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	cfg.Limits = cfg.Limits.withDefaults()
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{
		cfg:       cfg,
		be:        be,
		mux:       http.NewServeMux(),
		hub:       newHub(),
		limiter:   newRateLimiter(cfg.Clock, cfg.Limits.OpRate, cfg.Limits.OpBurst),
		ipLimiter: newRateLimiter(cfg.Clock, cfg.Limits.IPRate, cfg.Limits.IPBurst),
		log:       cfg.Logger,
	}
	s.ops = s.mailboxOps()
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET "+relayproto.PathHealth, s.handleHealth)
	s.mux.HandleFunc("GET "+relayproto.PathConnect, s.handleConnect)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(relayproto.Health{OK: true, Version: relayproto.Version})
}
