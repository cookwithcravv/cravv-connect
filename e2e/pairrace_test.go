package e2e

import (
	"testing"
	"time"
)

// The joiner finalizes first and sends at once, while the creator's human is
// still choosing an alias: the relay answers not_allowed because the creator
// has not allowed the joiner yet. That must not leave the joiner thinking it
// was paused: once the creator finalizes, the message is delivered and
// neither side shows a pause.
func TestSendBeforePeerFinalizes(t *testing.T) {
	t.Parallel()
	r := NewRelay(t)
	a := NewNode(t, r, "alice", NodeOptions{AdminToken: AdminToken})
	b := NewNode(t, r, "bob", NodeOptions{})
	var id string
	PairBetween(t, a, b, PairOptions{}, func() {
		b.WaitOnline()
		sb, _ := b.Session("codex")
		id = sendChat(t, sb, "alice", "sent before alice finalized")
		// The send loop tries at once; give the relay time to refuse it.
		time.Sleep(500 * time.Millisecond)
	})
	sa, _ := a.Session("claude")
	WaitItem(t, sa, wait, "early chat at alice", isChat(id))
	for _, v := range []struct {
		n     *Node
		alias string
	}{{a, "bob"}, {b, "alice"}} {
		Eventually(t, wait, v.n.Name+" sees no pause", func() bool {
			pv, ok := v.n.PeerView(v.alias)
			return ok && !pv.Paused && !pv.PausedByPeer
		})
	}
}
