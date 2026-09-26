package e2e

import (
	"testing"
	"time"
)

// A paired machine's view carries when it was last heard from.
func TestMachinesShowLastSeen(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	if v, _ := b.PeerView("alice"); !v.LastSeen.IsZero() {
		t.Fatalf("last seen %v before any traffic", v.LastSeen)
	}
	lead := a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	Connect(t, lead, "bob/trainer", "messages", "")
	Eventually(t, 10*time.Second, "bob hears from alice", func() bool {
		v, _ := b.PeerView("alice")
		return !v.LastSeen.IsZero() && time.Since(v.LastSeen) < time.Minute
	})
}
