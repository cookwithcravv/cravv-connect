package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
)

// Options configures a Server.
type Options struct {
	Clock        core.Clock
	Killed       func() bool          // reports the kill switch; nil means never killed
	OnDisconnect func(session string) // called after a connection with a session closes
	Logger       *slog.Logger
}

type method struct {
	h    Handler
	gate Gate
}

// Server is the method registry plus connection handling. Gates are enforced
// here, centrally, so no handler can forget them.
type Server struct {
	opts    Options
	mu      sync.RWMutex
	methods map[string]method
	conns   sync.WaitGroup
}

// NewServer returns a Server with no methods registered.
func NewServer(opts Options) *Server {
	if opts.Clock == nil {
		opts.Clock = core.SystemClock{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Server{opts: opts, methods: map[string]method{}}
}

// Register adds a method. Registering the same name twice panics, because it
// is always a wiring bug.
func (s *Server) Register(name string, h Handler, gate Gate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.methods[name]; dup {
		panic("ipc: duplicate method " + name)
	}
	s.methods[name] = method{h: h, gate: gate}
}

// Methods returns the registered method names and gates (for tests and docs).
func (s *Server) Methods() map[string]Gate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Gate, len(s.methods))
	for k, m := range s.methods {
		out[k] = m.gate
	}
	return out
}

// Serve accepts connections until ctx is cancelled or ln fails. It closes ln
// and waits for every connection to finish before returning.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.conns.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.conns.Add(1)
		go func() {
			defer s.conns.Done()
			s.ServeConn(ctx, conn)
		}()
	}
}

// ServeConn handles one connection until the peer closes it or ctx ends.
// Requests are handled concurrently; responses are written one line at a time.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	cctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(cctx, func() { conn.Close() })
	cs := NewConnState(s.opts.Clock)
	var (
		wmu      sync.Mutex
		inflight sync.WaitGroup
	)
	write := func(r Response) {
		b, err := json.Marshal(r)
		if err != nil {
			s.opts.Logger.Error("ipc: marshal response", "err", err)
			return
		}
		wmu.Lock()
		defer wmu.Unlock()
		if _, err := conn.Write(append(b, '\n')); err != nil {
			s.opts.Logger.Debug("ipc: write response", "err", err)
		}
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), MaxLineBytes)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil || req.Method == "" {
			write(Response{JSONRPC: Version, ID: json.RawMessage("null"), Error: &Error{
				Code: CodeParseError, Message: "parse error", Data: &ErrorData{Kind: KindBadRequest}}})
			continue
		}
		inflight.Add(1)
		go func(req Request) {
			defer inflight.Done()
			resp := s.dispatch(cctx, cs, req)
			if len(req.ID) == 0 {
				return // notification: no response
			}
			write(resp)
		}(req)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		s.opts.Logger.Debug("ipc: connection read ended", "err", err)
	}
	cancel()
	inflight.Wait()
	stop()
	conn.Close()
	if name := cs.Session(); name != "" && s.opts.OnDisconnect != nil {
		s.opts.OnDisconnect(name)
	}
}

// dispatch runs gates and the handler for one request.
func (s *Server) dispatch(ctx context.Context, cs *ConnState, req Request) (resp Response) {
	resp = Response{JSONRPC: Version, ID: req.ID}
	defer func() {
		if r := recover(); r != nil {
			s.opts.Logger.Error("ipc: handler panic", "method", req.Method, "panic", r)
			resp.Result = nil
			resp.Error = &Error{Code: CodeServerError, Message: "internal error", Data: &ErrorData{Kind: KindInternal}}
		}
	}()
	s.mu.RLock()
	m, ok := s.methods[req.Method]
	s.mu.RUnlock()
	if !ok {
		resp.Error = &Error{Code: CodeMethodNotFound, Message: fmt.Sprintf("unknown method %q", req.Method),
			Data: &ErrorData{Kind: KindBadRequest}}
		return resp
	}
	if err := s.checkGate(cs, m.gate); err != nil {
		resp.Error = toWire(err)
		return resp
	}
	result, err := m.h(ctx, cs, req.Params)
	if err != nil {
		resp.Error = toWire(err)
		return resp
	}
	if result == nil {
		result = Empty{}
	}
	b, err := json.Marshal(result)
	if err != nil {
		resp.Error = toWire(fmt.Errorf("marshal result: %w", err))
		return resp
	}
	resp.Result = b
	return resp
}

// checkGate enforces, in order: kill switch, session, unlock.
func (s *Server) checkGate(cs *ConnState, g Gate) error {
	if g&GateAllowWhenKilled == 0 && s.opts.Killed != nil && s.opts.Killed() {
		return core.ErrKilled
	}
	if g&GateSession != 0 && cs.Session() == "" {
		return core.ErrNoSession
	}
	if g&GateUnlock != 0 && !cs.Unlocked() {
		return core.ErrAuthRequired
	}
	return nil
}
