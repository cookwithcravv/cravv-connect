package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func newPipeServer() *Server {
	s := NewServer(Options{})
	s.Register("unlock", func(_ context.Context, cs *ConnState, _ json.RawMessage) (any, error) {
		return UnlockResult{ExpiresAt: cs.Unlock(core.UnlockTTL)}, nil
	}, GateNone)
	s.Register("secret", func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return IDResult{ID: "s3cret"}, nil
	}, GateUnlock)
	return s
}

// Each pipe is its own connection: unlocking one leaves the other locked.
func TestPipeConnectionsHaveTheirOwnState(t *testing.T) {
	s := newPipeServer()
	ctx := context.Background()
	a, _ := s.Pipe(ctx)
	defer a.Close()
	b, _ := s.Pipe(ctx)
	defer b.Close()
	if err := a.Call(ctx, "unlock", nil, nil); err != nil {
		t.Fatal(err)
	}
	var r IDResult
	if err := a.Call(ctx, "secret", nil, &r); err != nil || r.ID != "s3cret" {
		t.Fatalf("unlocked pipe: %v %+v", err, r)
	}
	if err := b.Call(ctx, "secret", nil, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("other pipe: err = %v, want ErrAuthRequired", err)
	}
}

func TestPipeEndsWhenClientCloses(t *testing.T) {
	s := newPipeServer()
	c, done := s.Pipe(context.Background())
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeConn did not return after the client closed")
	}
}

func TestPipeEndsWithContext(t *testing.T) {
	s := newPipeServer()
	ctx, cancel := context.WithCancel(context.Background())
	c, done := s.Pipe(ctx)
	defer c.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeConn did not return after ctx ended")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client did not see the connection end")
	}
	if err := c.Call(context.Background(), "unlock", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after end: %v, want ErrClosed", err)
	}
}
