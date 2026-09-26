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
	"sync/atomic"

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
	active  atomic.Int64 // requests running on all connections
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
// and waits for every connection to finish before returning. A connection
// from a process of another user (by the socket's peer credentials) is
// closed at once.
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
		if err := verifyPeer(conn); err != nil {
			s.opts.Logger.Warn("ipc: connection refused", "err", err)
			conn.Close()
			continue
		}
		s.conns.Add(1)
		go func() {
			defer s.conns.Done()
			s.ServeConn(ctx, conn)
		}()
	}
}

// MaxInflight caps the requests one connection may have running at once.
// Requests over the cap get an error of kind busy.
const MaxInflight = 32

// running tracks one in-flight request so $/cancel can cancel it.
type running struct{ cancel context.CancelFunc }

// ServeConn handles one connection until the peer closes it or ctx ends.
// Requests are handled concurrently (at most MaxInflight at a time); responses
// are written one line at a time. A $/cancel notification cancels the context
// of the request it names.
func (s *Server) ServeConn(ctx context.Context, conn net.Conn) {
	cctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(cctx, func() { conn.Close() })
	cs := NewConnState(s.opts.Clock)
	var (
		wmu      sync.Mutex
		inflight sync.WaitGroup
		rmu      sync.Mutex
		active   = map[string]*running{}
		count    int
	)
	write := func(r Response) {
		b := s.encodeResponse(r)
		if b == nil {
			return
		}
		wmu.Lock()
		defer wmu.Unlock()
		if _, err := conn.Write(b); err != nil {
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
		if req.Method == MethodCancel {
			var p CancelParams
			if json.Unmarshal(req.Params, &p) == nil {
				rmu.Lock()
				if r, ok := active[idKey(p.ID)]; ok {
					r.cancel()
				}
				rmu.Unlock()
			}
			if len(req.ID) > 0 {
				write(Response{JSONRPC: Version, ID: req.ID, Result: json.RawMessage("{}")})
			}
			continue
		}
		key := idKey(req.ID)
		rmu.Lock()
		if count >= MaxInflight {
			rmu.Unlock()
			if len(req.ID) > 0 {
				write(Response{JSONRPC: Version, ID: req.ID, Error: toWire(ErrBusy)})
			}
			continue
		}
		count++
		s.active.Add(1)
		rctx, rcancel := context.WithCancel(cctx)
		r := &running{cancel: rcancel}
		if key != "" {
			active[key] = r
		}
		rmu.Unlock()
		inflight.Add(1)
		go func(req Request) {
			defer inflight.Done()
			resp := s.dispatch(rctx, cs, req)
			rmu.Lock()
			count--
			s.active.Add(-1)
			if active[key] == r {
				delete(active, key)
			}
			rmu.Unlock()
			rcancel()
			if len(req.ID) == 0 {
				return // notification: no response
			}
			write(resp)
		}(req)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		if errors.Is(err, bufio.ErrTooLong) {
			write(Response{JSONRPC: Version, ID: json.RawMessage("null"), Error: &Error{
				Code: CodeServerError, Message: fmt.Sprintf("request line longer than %d bytes", MaxLineBytes),
				Data: &ErrorData{Kind: KindTooLarge}}})
		}
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

// idKey normalizes a JSON-RPC id for the cancel table.
func idKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

// encodeResponse encodes r as one line without HTML escaping. A line longer
// than MaxLineBytes would break the client's stream, so it is replaced by an
// error of kind too_large for the same id.
func (s *Server) encodeResponse(r Response) []byte {
	b, err := encodeLine(r)
	if err != nil {
		s.opts.Logger.Error("ipc: marshal response", "err", err)
		r = Response{JSONRPC: Version, ID: r.ID, Error: &Error{
			Code: CodeServerError, Message: "internal error", Data: &ErrorData{Kind: KindInternal}}}
		b, _ = encodeLine(r)
		return b
	}
	if len(b) > MaxLineBytes {
		s.opts.Logger.Warn("ipc: response too large", "bytes", len(b))
		b, _ = encodeLine(Response{JSONRPC: Version, ID: r.ID, Error: &Error{
			Code:    CodeServerError,
			Message: fmt.Sprintf("response of %d bytes is over the %d byte limit; ask for less", len(b), MaxLineBytes),
			Data:    &ErrorData{Kind: KindTooLarge},
		}})
	}
	return b
}

// encodeLine marshals v as JSON followed by a newline, without escaping
// '<', '>' and '&' (which would cost 6 bytes each in wrapped peer text).
func encodeLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
	b, err := encodeLine(result)
	if err != nil {
		resp.Error = toWire(fmt.Errorf("marshal result: %w", err))
		return resp
	}
	resp.Result = bytes.TrimSuffix(b, []byte("\n"))
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
