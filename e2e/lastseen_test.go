package e2e

import (
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/cli"
)

// A paired machine's view carries when it was last heard from, and link traffic
// moves it forward. Pairing itself may already count as contact (finalize sends
// control.resumed), so the test compares against a baseline instead of zero.
func TestMachinesShowLastSeen(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	v, _ := b.PeerView("alice")
	before := v.LastSeen
	time.Sleep(20 * time.Millisecond)
	lead := a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	Connect(t, lead, "bob/trainer", "messages", "")
	Eventually(t, 10*time.Second, "bob hears from alice after the link request", func() bool {
		v, _ := b.PeerView("alice")
		return v.LastSeen.After(before) && time.Since(v.LastSeen) < time.Minute
	})
}

// status reports the daemon's version, so a CLI of another version (after
// an upgrade) knows to restart it.
func TestStatusReportsDaemonVersion(t *testing.T) {
	t.Parallel()
	n := NewNode(t, NewRelay(t), "solo", NodeOptions{})
	if got := n.Status().Version; got != cli.BuildVersion() {
		t.Fatalf("status version %q, want %q", got, cli.BuildVersion())
	}
}
