package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

func ok(context.Context, *ConnState, json.RawMessage) (any, error) { return Empty{}, nil }

// A connection a run token bound may call RunMethods only, whatever the gates.
func TestRunBoundConnectionsUseOnlyRunMethods(t *testing.T) {
	s := NewServer(Options{})
	s.Register(MethodSessionRunBind, func(_ context.Context, cs *ConnState, _ json.RawMessage) (any, error) {
		cs.SetShared("S-run")
		cs.SetRunBound("")
		return Empty{}, nil
	}, GateNone)
	for _, m := range []string{MethodInboxCheck, MethodInboxWait, MethodChatSend, MethodTaskComplete, MethodLinks,
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
	for _, m := range []string{MethodChatSend, MethodTaskComplete, MethodLinks, MethodSessionRunBind} {
		if err := c.Call(ctx, m, nil, nil); err != nil {
			t.Errorf("%s on a run's connection: %v", m, err)
		}
	}
	// The host owns a managed session's inbox (its queue): a run reading it
	// would take items from the queue.
	for _, m := range []string{MethodInboxCheck, MethodInboxWait, MethodSessionShare, MethodLinkConnect, MethodLinkDecide, MethodAuthUnlock, MethodKill, MethodStatus, MethodOffersSet} {
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

// A connection from a process inside a managed run (by its peer PID) may
// only register and bind with its run token; until it binds, every other
// method is refused. Other connections are not affected.
func TestConnectionsFromARunMustBindFirst(t *testing.T) {
	peer := 42
	s := NewServer(Options{PeerPID: func(net.Conn) (int, error) { return peer, nil }})
	s.SetRunPeer(func(pid int) bool { return pid == 42 })
	s.Register(MethodSessionRegister, ok, GateNone)
	s.Register(MethodSessionRunBind, func(_ context.Context, cs *ConnState, _ json.RawMessage) (any, error) {
		cs.SetShared("S-run")
		cs.SetRunBound("/srv/proj")
		return Empty{}, nil
	}, GateNone)
	for _, m := range []string{MethodStatus, MethodChatSend, MethodKill} {
		s.Register(m, ok, GateNone)
	}
	ctx := context.Background()
	c, _ := s.Pipe(ctx)
	defer c.Close()
	for _, m := range []string{MethodStatus, MethodChatSend, MethodKill} {
		if err := c.Call(ctx, m, nil, nil); !errors.Is(err, core.ErrNotPermitted) {
			t.Errorf("%s from inside a run before binding: %v, want not_permitted", m, err)
		}
	}
	if err := c.Call(ctx, MethodSessionRegister, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, MethodSessionRunBind, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, MethodChatSend, nil, nil); err != nil {
		t.Fatalf("a bound run's connection uses the run methods: %v", err)
	}
	if err := c.Call(ctx, MethodStatus, nil, nil); !errors.Is(err, core.ErrNotPermitted) {
		t.Fatalf("status on a bound run's connection: %v", err)
	}

	peer = 7
	other, _ := s.Pipe(ctx)
	defer other.Close()
	if err := other.Call(ctx, MethodKill, nil, nil); err != nil {
		t.Fatalf("a connection from outside every run: %v", err)
	}
}

// The peer PID of a real unix socket connection is the dialing process.
func TestServeLearnsThePeerPID(t *testing.T) {
	dir, err := os.MkdirTemp("", "ipcpid")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "s.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{})
	s.Register(MethodStatus, ok, GateNone)
	seen := make(chan int, 1)
	s.SetRunPeer(func(pid int) bool {
		seen <- pid
		return false
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	c, err := DialContext(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, MethodStatus, nil, nil); err != nil {
		t.Fatal(err)
	}
	if pid := <-seen; pid != os.Getpid() {
		t.Fatalf("peer PID %d, want %d", pid, os.Getpid())
	}
	c.Close()
	cancel()
	<-done
}
