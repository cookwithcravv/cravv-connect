package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// The joiner finalizes first and sends at once, while the creator's human is
// still choosing an alias: the relay answers not_allowed because the creator
// has not allowed the joiner yet. That must not leave the joiner thinking it
// was paused: once the creator finalizes, the message (here a link request
// to a session alice shared before pairing) is delivered and neither side
// shows a pause.
func TestSendBeforePeerFinalizes(t *testing.T) {
	t.Parallel()
	r := NewRelay(t)
	a := NewNode(t, r, "alice", NodeOptions{AdminToken: AdminToken})
	b := NewNode(t, r, "bob", NodeOptions{})
	a.WaitOnline()
	a.Share("claude", "lead", "all-peers")
	ctx := context.Background()
	lead, err := a.Daemon.Shared().List(ctx, core.SessionOpen)
	if err != nil || len(lead) != 1 {
		t.Fatalf("alice's sessions %+v, %v", lead, err)
	}
	PairBetween(t, a, b, PairOptions{}, func() {
		b.WaitOnline()
		b.Share("codex", "worker", "private")
		mine, err := b.Daemon.Shared().List(ctx, core.SessionOpen)
		if err != nil || len(mine) != 1 {
			t.Fatalf("bob's sessions %+v, %v", mine, err)
		}
		body := core.LinkRequestBody{LinkID: core.NewID(), FromSession: core.SessionRef{ID: mine[0].ID, Name: "worker"},
			ToSessionID: lead[0].ID, ProposedPermission: core.PermMessages}
		if _, err := b.Daemon.Outbound().SendEnvelope(ctx, a.Daemon.Identity().MachineID(), core.KindLinkRequest, "", body); err != nil {
			t.Fatal(err)
		}
		// The send loop tries at once; give the relay time to refuse it.
		time.Sleep(500 * time.Millisecond)
	})
	a.WaitLink(wait, "early link request at alice", func(v ipc.LinkView) bool {
		return v.State == "pending" && v.Direction == "in" && v.RemoteSession == "worker"
	})
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
