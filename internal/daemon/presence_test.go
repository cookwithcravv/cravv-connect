package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// tickBoth runs one presence round on both machines and delivers everything.
func tickBoth(t *testing.T, n *v2Net, a, b *v2Node) {
	t.Helper()
	ctx := context.Background()
	if err := a.presence.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.presence.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	n.pump()
}

func TestPresenceKeepsLiveLinksOpen(t *testing.T) {
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	for range 20 { // 10 minutes of heartbeats
		tickBoth(t, n, a, b)
		n.clock.Advance(core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive {
		t.Fatalf("alice %+v", got)
	}
	if got := b.linkOf(t, a, l.bLink.ID); got.State != store.LinkActive {
		t.Fatalf("bob %+v", got)
	}
	if pings := len(n.sent(core.KindPresencePing)); pings != 40 {
		t.Fatalf("%d pings, want 2 machines x 20 rounds", pings)
	}
}

func TestPresenceTimeoutWhenMachineDrops(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	tickBoth(t, n, a, b) // both sides have fresh evidence at t0
	n.setDown(b, true)
	for elapsed := time.Duration(0); elapsed < core.PresenceTimeout; elapsed += core.PresenceInterval {
		n.clock.Advance(core.PresenceInterval)
		if err := a.presence.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive {
		t.Fatalf("closed at exactly the timeout: %+v", got)
	}
	n.clock.Advance(time.Second)
	if err := a.presence.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := a.linkOf(t, b, l.aLink.ID)
	if got.State != store.LinkClosed || got.Reason != core.ClosePresenceTimeout {
		t.Fatalf("after the timeout %+v", got)
	}
	items := a.notices(t, l.lead.Session.ID)
	if last := items[len(items)-1]; last.Kind != core.KindLinkClosed {
		t.Fatalf("lead was not told: %+v", last)
	}
	// Bob comes back: the queued link.closed converges his side.
	n.setDown(b, false)
	n.pump()
	if got := b.linkOf(t, a, l.bLink.ID); got.State != store.LinkClosed || got.Reason != core.ClosePresenceTimeout {
		t.Fatalf("bob after reconnect %+v", got)
	}
}

func TestPongLeavingLinkOutClosesIt(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	// Bob lost the link without telling alice (split brain).
	if _, err := b.st.UpdateLink(ctx, a.id, l.bLink.ID, func(x *store.Link) error {
		x.State = store.LinkClosed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.presence.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkClosed || got.Reason != core.ClosePresenceTimeout {
		t.Fatalf("alice kept a link bob does not have: %+v", got)
	}
}

// Review focus: bob accepts and pings alice before alice has processed
// link.accepted (pings bypass the outbox). Alice's link is still pending;
// her pong must count it as open, or bob would close a link he just accepted.
func TestPongCountsPendingOutgoingLinks(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	if _, err := b.links.Decide(ctx, b.linkOf(t, a, out.ID).Num, true, "", AuthPassword); err != nil {
		t.Fatal(err)
	}
	// link.accepted is queued; the ping goes out directly and is answered at once.
	if err := b.presence.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.linkOf(t, b, out.ID); got.State != store.LinkPending {
		t.Fatalf("test setup: alice already %s", got.State)
	}
	if got := b.linkOf(t, a, out.ID); got.State != store.LinkActive {
		t.Fatalf("bob closed the link he just accepted: %+v", got)
	}
	n.pump()
	if got := a.linkOf(t, b, out.ID); got.State != store.LinkActive {
		t.Fatalf("alice after link.accepted %+v", got)
	}
}

// Review focus: presence frames older than core.PresenceMaxAge (queued by
// the relay while a machine was offline) must not keep a dead link alive
// or be answered.
func TestStalePresenceFramesAreIgnored(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	if err := a.presence.Tick(ctx); err != nil { // seeds alice's evidence and sends a ping
		t.Fatal(err)
	}
	n.pump()
	pongs := len(n.sent(core.KindPresencePong))
	old := n.clock.Now().Add(-core.PresenceMaxAge - time.Second).UnixMilli()
	bobAtAlice, aliceAtBob := a.peerRec(b), b.peerRec(a)
	stalePing, err := core.NewEnvelope(n.clock, a.id, b.id, core.KindPresencePing, core.PresencePingBody{TS: old, LinkIDs: []string{l.aLink.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.presence.HandlePing(ctx, aliceAtBob, stalePing); err != nil {
		t.Fatal(err)
	}
	if got := len(n.sent(core.KindPresencePong)); got != pongs {
		t.Fatal("a stale ping was answered")
	}
	n.setDown(b, true)
	n.clock.Advance(core.PresenceTimeout)
	// A stale pong naming the link arrives just before the deadline.
	stalePong, err := core.NewEnvelope(n.clock, b.id, a.id, core.KindPresencePong, core.PresencePongBody{TS: old, LinkIDsOpen: []string{l.aLink.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.presence.HandlePong(ctx, bobAtAlice, stalePong); err != nil {
		t.Fatal(err)
	}
	n.clock.Advance(time.Second)
	if err := a.presence.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkClosed {
		t.Fatalf("a stale pong kept the link alive: %+v", got)
	}
}

// A daemon restart forgets presence evidence; a link seen for the first
// time gets the full timeout from then, not an instant close.
func TestFirstTickSeedsEvidence(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	n.setDown(b, true)
	n.clock.Advance(time.Hour) // the link was created an hour ago
	fresh := NewPresenceService(a.st, a.st, a.links, a.sender, n.clock, nil)
	if err := fresh.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive {
		t.Fatalf("closed on the first tick after a restart: %+v", got)
	}
}
