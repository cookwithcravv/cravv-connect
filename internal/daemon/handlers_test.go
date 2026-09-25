package daemon

import (
	"context"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestHandlerRegistry(t *testing.T) {
	r := NewHandlerRegistry()
	if _, ok := r.Lookup(core.KindChat); ok {
		t.Fatal("empty registry returned a handler")
	}
	var got []string
	r.Register(core.KindChat, HandlerFunc(func(_ context.Context, _ store.Peer, env core.Envelope) error {
		got = append(got, "first:"+env.ID)
		return nil
	}))
	r.Register(core.KindChat, HandlerFunc(func(_ context.Context, _ store.Peer, env core.Envelope) error {
		got = append(got, "second:"+env.ID)
		return nil
	}))
	h, ok := r.Lookup(core.KindChat)
	if !ok {
		t.Fatal("registered handler not found")
	}
	if err := h.Handle(context.Background(), store.Peer{}, core.Envelope{ID: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "second:x" {
		t.Fatalf("got %v, want the replacing handler only", got)
	}
	if _, ok := r.Lookup(core.KindTaskCreate); ok {
		t.Fatal("unregistered kind returned a handler")
	}
}
