package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func ok(context.Context, *ConnState, json.RawMessage) (any, error) { return Empty{}, nil }

// A connection a run token bound may call RunMethods only, whatever the gates.
func TestRunBoundConnectionsUseOnlyRunMethods(t *testing.T) {
	s := NewServer(Options{})
	s.Register(MethodSessionRunBind, func(_ context.Context, cs *ConnState, _ json.RawMessage) (any, error) {
		cs.SetShared("S-run")
		cs.SetRunBound()
		return Empty{}, nil
	}, GateNone)
	for _, m := range []string{MethodInboxCheck, MethodChatSend, MethodTaskComplete, MethodLinks,
		MethodSessionShare, MethodLinkConnect, MethodLinkDecide, MethodAuthUnlock, MethodKill, MethodStatus, MethodOffersSet} {
		s.Register(m, ok, GateNone)
	}
	ctx := context.Background()
	c, _ := s.Pipe(ctx)
	defer c.Close()
	if err := c.Call(ctx, MethodKill, nil, nil); err != nil {
		t.Fatalf("before binding every method works: %v", err)
	}
	if err := c.Call(ctx, MethodSessionRunBind, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{MethodInboxCheck, MethodChatSend, MethodTaskComplete, MethodLinks, MethodSessionRunBind} {
		if err := c.Call(ctx, m, nil, nil); err != nil {
			t.Errorf("%s on a run's connection: %v", m, err)
		}
	}
	for _, m := range []string{MethodSessionShare, MethodLinkConnect, MethodLinkDecide, MethodAuthUnlock, MethodKill, MethodStatus, MethodOffersSet} {
		if err := c.Call(ctx, m, nil, nil); !errors.Is(err, core.ErrNotPermitted) {
			t.Errorf("%s on a run's connection: %v, want not_permitted", m, err)
		}
	}
	other, _ := s.Pipe(ctx)
	defer other.Close()
	if err := other.Call(ctx, MethodKill, nil, nil); err != nil {
		t.Fatalf("another connection is not affected: %v", err)
	}
}

func TestOnCloseRunsWhenTheConnectionEnds(t *testing.T) {
	s := NewServer(Options{})
	closed := make(chan string, 4)
	s.Register("hold", func(_ context.Context, cs *ConnState, _ json.RawMessage) (any, error) {
		cs.OnClose(func() { closed <- "first" })
		cs.OnClose(func() { closed <- "second" })
		return Empty{}, nil
	}, GateNone)
	ctx := context.Background()
	c, done := s.Pipe(ctx)
	if err := c.Call(ctx, "hold", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-closed:
		t.Fatalf("ran %q before the connection ended", got)
	default:
	}
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection did not end")
	}
	if a, b := <-closed, <-closed; a != "second" || b != "first" || len(closed) != 0 {
		t.Fatalf("closers ran %q, %q (want newest first, once)", a, b)
	}
}
