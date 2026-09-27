package daemon

import (
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// rateLimiterPrune is how many keys a RateLimiter holds before it drops
// expired windows.
const rateLimiterPrune = 1024

// RateLimiter allows at most n events per key in each fixed window. The
// window for a key starts at its first event. It is safe for concurrent use.
type RateLimiter struct {
	clock  core.Clock
	n      int
	window time.Duration

	mu sync.Mutex
	m  map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

// NewRateLimiter allows n events per key per window.
func NewRateLimiter(clock core.Clock, n int, window time.Duration) *RateLimiter {
	return &RateLimiter{clock: clock, n: n, window: window, m: map[string]*rateWindow{}}
}

// Allow records an event for key and reports whether it is within the limit.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	w, ok := r.m[key]
	if !ok || now.Sub(w.start) >= r.window {
		if !ok && len(r.m) >= rateLimiterPrune {
			r.pruneLocked(now)
		}
		r.m[key] = &rateWindow{start: now, count: 1}
		return true
	}
	if w.count >= r.n {
		return false
	}
	w.count++
	return true
}

func (r *RateLimiter) pruneLocked(now time.Time) {
	for k, w := range r.m {
		if now.Sub(w.start) >= r.window {
			delete(r.m, k)
		}
	}
}
