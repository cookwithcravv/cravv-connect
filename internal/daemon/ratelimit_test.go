package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

func TestRateLimiterWindowPerKey(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	r := NewRateLimiter(clock, 3, time.Minute)
	for i := range 3 {
		if !r.Allow("a") {
			t.Fatalf("event %d refused", i+1)
		}
	}
	if r.Allow("a") {
		t.Fatal("fourth event in the window allowed")
	}
	if !r.Allow("b") {
		t.Fatal("keys must not share a window")
	}
	clock.Advance(59 * time.Second)
	if r.Allow("a") {
		t.Fatal("allowed before the window ended")
	}
	clock.Advance(time.Second)
	if !r.Allow("a") {
		t.Fatal("refused after the window ended")
	}
}

func TestRateLimiterPrunesExpiredKeys(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	r := NewRateLimiter(clock, 1, time.Minute)
	for i := range rateLimiterPrune {
		r.Allow(fmt.Sprint(i))
	}
	clock.Advance(time.Minute)
	r.Allow("new")
	r.mu.Lock()
	n := len(r.m)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d keys kept after pruning, want 1", n)
	}
}
