package daemon

import (
	"context"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Handler processes one opened, verified, deduplicated envelope from a paired peer.
type Handler interface {
	Handle(ctx context.Context, peer store.Peer, env core.Envelope) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, peer store.Peer, env core.Envelope) error

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, peer store.Peer, env core.Envelope) error {
	return f(ctx, peer, env)
}

// HandlerRegistry maps message kinds to handlers. New kinds register here; Inbound never changes.
type HandlerRegistry struct {
	mu sync.RWMutex
	m  map[core.Kind]Handler
}

// NewHandlerRegistry returns an empty registry.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{m: make(map[core.Kind]Handler)}
}

// Register sets the handler for k, replacing any earlier one.
func (r *HandlerRegistry) Register(k core.Kind, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[k] = h
}

// Lookup returns the handler for k.
func (r *HandlerRegistry) Lookup(k core.Kind) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.m[k]
	return h, ok
}
