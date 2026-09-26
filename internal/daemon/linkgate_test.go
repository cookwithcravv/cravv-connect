package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestPermissionPolicyTable(t *testing.T) {
	var p PermissionPolicy
	cases := []struct {
		perm core.Permission
		kind core.Kind
		want Decision
	}{
		{core.PermMessages, core.KindChat, DecisionDeliver},
		{core.PermMessages, core.KindFileOffer, DecisionDeliver},
		{core.PermMessages, core.KindTaskUpdate, DecisionDeliver},
		{core.PermMessages, core.KindTaskCancel, DecisionDeliver},
		{core.PermMessages, core.KindTaskCreate, DecisionReject},
		{core.PermTasksAsk, core.KindTaskCreate, DecisionHold},
		{core.PermTasksAuto, core.KindTaskCreate, DecisionDeliver},
		{core.PermTasksAuto, core.KindChat, DecisionDeliver},
		{"", core.KindChat, DecisionReject},
		{"autonomous", core.KindTaskCreate, DecisionReject},
	}
	for _, c := range cases {
		if got := p.Decide(c.perm, c.kind); got != c.want {
			t.Errorf("Decide(%q, %s) = %s, want %s", c.perm, c.kind, got, c.want)
		}
	}
}

// gateCalls records what the gate did with one envelope.
type gateCalls struct {
	inner, rejected      int
	unknown, unsupported int
	seen                 int // valid link traffic from the peer
	link                 store.Link
	decision             Decision
}

func (c *gateCalls) UnknownLink(context.Context, store.Peer, string) { c.unknown++ }
func (c *gateCalls) Unsupported(context.Context, store.Peer)         { c.unsupported++ }

func TestLinkGate(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages) // bob's side: alice may send messages
	aliceAtBob := b.peerRec(a)
	stranger := store.Peer{MachineID: "someone-else", Alias: "eve"}
	pending, err := a.links.Connect(ctx, l.lead.Session.ID, "bob/trainer", core.PermTasksAuto, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	run := func(peer store.Peer, kind core.Kind, linkID string) *gateCalls {
		c := &gateCalls{}
		record := func(counter *int) Handler {
			return HandlerFunc(func(ctx context.Context, _ store.Peer, _ core.Envelope) error {
				*counter++
				c.link, _ = LinkFrom(ctx)
				c.decision, _ = DecisionFrom(ctx)
				return nil
			})
		}
		g := LinkGate{Links: b.st, Sessions: b.shared, Replies: c, Inner: record(&c.inner), OnReject: record(&c.rejected),
			Seen: func(p store.Peer) { c.seen++ }}
		env, err := core.NewEnvelope(n.clock, peer.MachineID, b.id, kind, core.ChatBody{Text: "x"})
		if err != nil {
			t.Fatal(err)
		}
		env.LinkID = linkID
		if err := g.Handle(ctx, peer, env); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := run(aliceAtBob, core.KindChat, ""); c.unsupported != 1 || c.inner+c.rejected+c.unknown+c.seen != 0 {
		t.Errorf("link-less chat: %+v", c)
	}
	if c := run(aliceAtBob, core.KindChat, core.NewID()); c.unknown != 1 || c.inner+c.seen != 0 {
		t.Errorf("unknown link: %+v", c)
	}
	if c := run(aliceAtBob, core.KindChat, pending.ID); c.unknown != 1 || c.inner+c.seen != 0 {
		t.Errorf("pending link: %+v", c)
	}
	if c := run(stranger, core.KindChat, l.bLink.ID); c.unknown != 1 || c.inner+c.seen != 0 {
		t.Errorf("another machine using alice's link id: %+v", c)
	}
	c := run(aliceAtBob, core.KindChat, l.bLink.ID)
	if c.inner != 1 || c.seen != 1 || c.decision != DecisionDeliver || c.link.ID != l.bLink.ID || c.link.Session != l.worker.Session.ID {
		t.Errorf("chat on the active link: %+v", c)
	}
	if c := run(aliceAtBob, core.KindTaskCreate, l.bLink.ID); c.rejected != 1 || c.seen != 1 || c.inner != 0 || c.decision != DecisionReject {
		t.Errorf("task on a messages link: %+v", c)
	}
	for perm, want := range map[core.Permission]Decision{core.PermTasksAsk: DecisionHold, core.PermTasksAuto: DecisionDeliver} {
		if _, err := b.links.SetPermission(ctx, "", l.bLink.Num, perm, AuthPassword); err != nil {
			t.Fatal(err)
		}
		if c := run(aliceAtBob, core.KindTaskCreate, l.bLink.ID); c.inner != 1 || c.decision != want {
			t.Errorf("task at %s: %+v", perm, c)
		}
	}
	// The local session away: still delivered (it queues for the away grace).
	if err := b.shared.Detach(ctx, l.worker.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	if c := run(aliceAtBob, core.KindChat, l.bLink.ID); c.inner != 1 {
		t.Errorf("chat to an away session: %+v", c)
	}
	// Closed session: the link is closed with it, so the gate drops.
	n.clock.Advance(core.AwayGrace + time.Second)
	if _, err := b.shared.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if c := run(aliceAtBob, core.KindChat, l.bLink.ID); c.unknown != 1 || c.inner != 0 {
		t.Errorf("chat after the session closed: %+v", c)
	}
}

func TestLinkRepliesAreRateLimited(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	r := NewLinkReplies(a.sender, n.clock, nil)
	bob := a.peerRec(b)
	for range 3 {
		r.Unsupported(ctx, bob)
		r.UnknownLink(ctx, bob, "L1-NOT-AN-ID")
	}
	link := core.NewID()
	for range 3 {
		r.UnknownLink(ctx, bob, link)
	}
	if got := len(n.sent(core.KindControlUnsupported)); got != 1 {
		t.Fatalf("%d control.unsupported, want 1 per hour", got)
	}
	if got := len(n.sent(core.KindLinkClosed)); got != 1 {
		t.Fatalf("%d unknown_link replies, want 1 per link per minute (and none for an invalid id)", got)
	}
	if b := v2Body[core.UnsupportedBody](t, n.sent(core.KindControlUnsupported)[0]); b.MinVersion != 2 {
		t.Fatalf("min_version %d", b.MinVersion)
	}
	n.clock.Advance(core.UnknownLinkReplyEvery)
	r.UnknownLink(ctx, bob, link)
	r.Unsupported(ctx, bob)
	if len(n.sent(core.KindLinkClosed)) != 2 || len(n.sent(core.KindControlUnsupported)) != 1 {
		t.Fatal("after a minute: one more unknown_link, still one unsupported")
	}
	n.clock.Advance(core.UnsupportedReplyEvery)
	r.Unsupported(ctx, bob)
	if len(n.sent(core.KindControlUnsupported)) != 2 {
		t.Fatal("after an hour: a second control.unsupported")
	}
}
