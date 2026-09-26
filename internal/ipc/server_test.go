package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func newGateServer(clock core.Clock, killed *bool) *Server {
	s := NewServer(Options{Clock: clock, Killed: func() bool { return *killed }})
	ok := func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}
	s.Register("open", ok, GateNone)
	s.Register("sess", ok, GateSession)
	s.Register("locked", ok, GateUnlock)
	s.Register("sess_locked", ok, GateSession|GateUnlock)
	s.Register("always", ok, GateAllowWhenKilled)
	s.Register("always_locked", ok, GateAllowWhenKilled|GateUnlock)
	return s
}

func call(s *Server, cs *ConnState, m string) error {
	resp := s.dispatch(context.Background(), cs, Request{JSONRPC: Version, ID: json.RawMessage("1"), Method: m})
	if resp.Error != nil {
		return fromWire(resp.Error)
	}
	return nil
}

func TestGateMatrix(t *testing.T) {
	type state struct{ session, unlocked, killed bool }
	cases := []struct {
		method string
		st     state
		want   error
	}{
		{"open", state{}, nil},
		{"sess", state{}, core.ErrNoSession},
		{"sess", state{session: true}, nil},
		{"locked", state{}, core.ErrAuthRequired},
		{"locked", state{unlocked: true}, nil},
		{"sess_locked", state{unlocked: true}, core.ErrNoSession},
		{"sess_locked", state{session: true}, core.ErrAuthRequired},
		{"sess_locked", state{session: true, unlocked: true}, nil},
		{"open", state{killed: true}, core.ErrKilled},
		{"sess", state{killed: true}, core.ErrKilled}, // kill is checked before session
		{"locked", state{killed: true, unlocked: true}, core.ErrKilled},
		{"always", state{killed: true}, nil},
		{"always_locked", state{killed: true}, core.ErrAuthRequired},
		{"always_locked", state{killed: true, unlocked: true}, nil},
	}
	for _, tc := range cases {
		clock := core.NewFakeClock(time.Unix(1_700_000_000, 0))
		killed := tc.st.killed
		s := newGateServer(clock, &killed)
		cs := NewConnState(clock)
		if tc.st.session {
			cs.SetSession("claude@proj", "/p")
		}
		if tc.st.unlocked {
			cs.Unlock(core.UnlockTTL)
		}
		err := call(s, cs, tc.method)
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s %+v: err = %v, want %v", tc.method, tc.st, err, tc.want)
		}
	}
}

func TestUnlockExpires(t *testing.T) {
	clock := core.NewFakeClock(time.Unix(1_700_000_000, 0))
	killed := false
	s := newGateServer(clock, &killed)
	cs := NewConnState(clock)
	until := cs.Unlock(core.UnlockTTL)
	if !until.Equal(clock.Now().Add(10 * time.Minute)) {
		t.Fatalf("until = %v", until)
	}
	clock.Advance(core.UnlockTTL - time.Second)
	if err := call(s, cs, "locked"); err != nil {
		t.Fatalf("inside window: %v", err)
	}
	clock.Advance(time.Second)
	if err := call(s, cs, "locked"); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("at expiry: %v", err)
	}
}

func TestUnlockIsPerConnection(t *testing.T) {
	clock := core.NewFakeClock(time.Unix(1_700_000_000, 0))
	killed := false
	s := newGateServer(clock, &killed)
	a, b := NewConnState(clock), NewConnState(clock)
	a.Unlock(core.UnlockTTL)
	if err := call(s, b, "locked"); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("unlock leaked across connections: %v", err)
	}
}

func TestUnknownMethodAndPanic(t *testing.T) {
	s := NewServer(Options{})
	s.Register("boom", func(context.Context, *ConnState, json.RawMessage) (any, error) { panic("x") }, GateNone)
	cs := NewConnState(core.SystemClock{})
	if err := call(s, cs, "nope"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("unknown method: %v", err)
	}
	var re *RemoteError
	if err := call(s, cs, "boom"); !errors.As(err, &re) || re.Kind != KindInternal {
		t.Fatalf("panic: %v", err)
	}
}

func TestDuplicateRegisterPanics(t *testing.T) {
	s := NewServer(Options{})
	h := func(context.Context, *ConnState, json.RawMessage) (any, error) { return nil, nil }
	s.Register("a", h, GateNone)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	s.Register("a", h, GateNone)
}

func TestTypedDecodesAndRejectsBadParams(t *testing.T) {
	type P struct {
		N int `json:"n"`
	}
	h := Typed(func(_ context.Context, _ *ConnState, p P) (any, error) { return p.N, nil })
	cs := NewConnState(core.SystemClock{})
	for _, raw := range []string{"", "null", "{}"} {
		if v, err := h(context.Background(), cs, json.RawMessage(raw)); err != nil || v != 0 {
			t.Fatalf("%q: %v %v", raw, v, err)
		}
	}
	if v, _ := h(context.Background(), cs, json.RawMessage(`{"n":7}`)); v != 7 {
		t.Fatalf("got %v", v)
	}
	if _, err := h(context.Background(), cs, json.RawMessage(`{"n":"x"}`)); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("bad params: %v", err)
	}
}

func TestSharedGate(t *testing.T) {
	clock := core.NewFakeClock(time.Unix(1_700_000_000, 0))
	stale := errors.New("session moved to another connection")
	var bound uint64
	s := NewServer(Options{Clock: clock, CheckShared: func(cs *ConnState) error {
		if cs.ID() != bound {
			return stale
		}
		return nil
	}})
	s.Register("shared", func(context.Context, *ConnState, json.RawMessage) (any, error) { return nil, nil }, GateShared)
	a, b := NewConnState(clock), NewConnState(clock)
	if a.ID() == b.ID() || a.ID() == 0 {
		t.Fatalf("connection IDs %d and %d must be distinct and non-zero", a.ID(), b.ID())
	}
	if err := call(s, a, "shared"); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("unshared connection: err = %v, want ErrNotShared", err)
	}
	a.SetShared("S1")
	bound = a.ID()
	if err := call(s, a, "shared"); err != nil {
		t.Fatalf("bound connection: %v", err)
	}
	b.SetShared("S1")
	if err := call(s, b, "shared"); err == nil || err.Error() != stale.Error() {
		t.Fatalf("connection that is not bound: err = %v", err)
	}
	bound = b.ID() // b reattached: a loses the session
	if err := call(s, a, "shared"); err == nil {
		t.Fatal("the old connection still acts as the session after a reattach")
	}
}
