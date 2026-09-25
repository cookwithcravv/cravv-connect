package relayserver

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

type bucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter is a token bucket per key (mailbox ID), refilled from the injected clock.
type rateLimiter struct {
	mu      sync.Mutex
	clock   core.Clock
	rate    float64
	burst   float64
	buckets map[string]*bucket
}

func newRateLimiter(clock core.Clock, rate float64, burst int) *rateLimiter {
	return &rateLimiter{clock: clock, rate: rate, burst: float64(burst), buckets: map[string]*bucket{}}
}

// allow takes one token for key. It always allows when rate < 0.
func (l *rateLimiter) allow(key string) bool {
	if l.rate < 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = min(l.burst, b.tokens+el*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictFull drops buckets that have refilled to the burst size. A dropped key starts
// again with a full bucket, so eviction never changes what allow decides; it only
// keeps idle keys from accumulating.
func (l *rateLimiter) evictFull() {
	if l.rate < 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// clientIP is the connection's remote IP. Proxy headers are not trusted.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
