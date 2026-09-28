package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
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

// tickAlice advances the clock by d and runs one presence round on alice.
func tickAlice(t *testing.T, n *v2Net, a *v2Node, d time.Duration) {
	t.Helper()
	n.clock.Advance(d)
	if err := a.presence.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// v2 spec 5: a machine that stops answering makes its links away after the
// presence timeout; they close (presence_timeout) only after the away
// grace, and the session is told then.
func TestPresenceTimeoutMarksAwayThenClosesAfterGrace(t *testing.T) {
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	tickBoth(t, n, a, b) // both sides have fresh evidence at t0
	n.setDown(b, true)
	for elapsed := time.Duration(0); elapsed < core.PresenceTimeout; elapsed += core.PresenceInterval {
		tickAlice(t, n, a, core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("away at exactly the timeout: %+v", got)
	}
	notices := len(a.notices(t, l.lead.Session.ID))
	tickAlice(t, n, a, time.Second)
	awayAt := n.clock.Now()
	got := a.linkOf(t, b, l.aLink.ID)
	if got.State != store.LinkActive || !got.PresenceAway.Equal(awayAt) {
		t.Fatalf("after the timeout: %+v", got)
	}
	// Away links are still pinged, so a machine that comes back is seen.
	pings := len(n.sent(core.KindPresencePing))
	for n.clock.Now().Sub(awayAt)+core.PresenceInterval <= core.AwayGrace {
		tickAlice(t, n, a, core.PresenceInterval)
	}
	if len(n.sent(core.KindPresencePing)) == pings {
		t.Fatal("an away link was not pinged")
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive {
		t.Fatalf("closed within the away grace: %+v", got)
	}
	if len(a.notices(t, l.lead.Session.ID)) != notices {
		t.Fatal("the session was told before the link closed")
	}
	tickAlice(t, n, a, core.PresenceInterval)
	got = a.linkOf(t, b, l.aLink.ID)
	if got.State != store.LinkClosed || got.Reason != core.ClosePresenceTimeout {
		t.Fatalf("after the away grace %+v", got)
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

// markAway takes bob off the network until alice marks the link away.
func markAway(t *testing.T, n *v2Net, a, b *v2Node, l v2Linked) {
	t.Helper()
	tickBoth(t, n, a, b)
	n.setDown(b, true)
	for a.linkOf(t, b, l.aLink.ID).PresenceAway.IsZero() {
		tickAlice(t, n, a, core.PresenceInterval)
	}
}

// A fresh pong brings an away link back to active.
func TestPresencePongClearsAway(t *testing.T) {
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	markAway(t, n, a, b, l)
	n.setDown(b, false)
	tickAlice(t, n, a, core.PresenceInterval) // ping
	n.pump()                                  // bob pongs
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("after a fresh pong %+v", got)
	}
	// The grace no longer runs: much later the link is still active.
	for range 30 {
		tickBoth(t, n, a, b)
		n.clock.Advance(core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive {
		t.Fatalf("closed after coming back: %+v", got)
	}
}

// Traffic on the link (a ping, or anything the link gate admits) brings
// an away link back too.
func TestPresenceTrafficClearsAway(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	markAway(t, n, a, b, l)
	n.setDown(b, false)
	a.presence.Traffic(ctx, a.linkOf(t, b, l.aLink.ID))
	if got := a.linkOf(t, b, l.aLink.ID); !got.PresenceAway.IsZero() {
		t.Fatalf("after traffic %+v", got)
	}

	markAway(t, n, a, b, l)
	n.setDown(b, false)
	if err := b.presence.Tick(ctx); err != nil { // bob pings alice
		t.Fatal(err)
	}
	n.pump()
	if got := a.linkOf(t, b, l.aLink.ID); !got.PresenceAway.IsZero() {
		t.Fatalf("after bob's ping %+v", got)
	}
}

// The away grace comes from the daemon: a managed session's link waits for
// the session's idle timeout instead.
func TestPresenceAwayGraceIsPerLink(t *testing.T) {
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	a.presence.SetGrace(func(context.Context, store.Link) time.Duration { return 2 * time.Hour })
	markAway(t, n, a, b, l)
	for range 4 * 60 / 2 { // an hour, in 30 second rounds
		tickAlice(t, n, a, core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive {
		t.Fatalf("closed before its grace: %+v", got)
	}
	for range 4*60/2 + 1 {
		tickAlice(t, n, a, core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkClosed || got.Reason != core.ClosePresenceTimeout {
		t.Fatalf("after its grace %+v", got)
	}
}

// Local sleep: when far more than a heartbeat passed since the last round
// (the laptop was asleep), this side has no evidence about anybody. It
// forgets the old evidence and pings first instead of marking links away,
// and it does not close away links on that round either.
func TestPresenceAfterLocalSleepPingsFirst(t *testing.T) {
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	tickBoth(t, n, a, b)
	pings := len(n.sent(core.KindPresencePing))
	tickAlice(t, n, a, time.Hour) // alice slept an hour; bob is fine
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("after waking %+v", got)
	}
	if len(n.sent(core.KindPresencePing)) == pings {
		t.Fatal("no ping after waking")
	}
	n.pump()
	tickAlice(t, n, a, core.PresenceInterval)
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("a round after waking %+v", got)
	}

	// An away link whose grace ran out during the sleep is pinged first too.
	markAway(t, n, a, b, l)
	n.setDown(b, false)
	tickAlice(t, n, a, 2*core.AwayGrace)
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive {
		t.Fatalf("closed on the round after a sleep: %+v", got)
	}
	n.pump()
	if got := a.linkOf(t, b, l.aLink.ID); !got.PresenceAway.IsZero() {
		t.Fatalf("the pong after waking did not clear away: %+v", got)
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
	for range 5 { // the presence timeout, one heartbeat at a time
		tickAlice(t, n, a, core.PresenceInterval)
	}
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
	if got := a.linkOf(t, b, l.aLink.ID); got.PresenceAway.IsZero() {
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
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("away on the first tick after a restart: %+v", got)
	}
}

// The daemon's grace per link: the away grace for a live session's link,
// the offer's idle timeout for a managed session's (the default when the
// offer is gone), so presence never closes a managed run's link sooner
// than idleness would.
func TestPresenceGraceByKind(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	clock := core.NewFakeClock(d2Epoch)
	sessions := NewSessionService(st, clock)
	live, err := sessions.Share(ctx, 1, ShareRequest{Agent: "claude", ProjectDir: "/w/p", Name: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	managed, err := sessions.CreateManaged(ctx, "trainer-ab12", "", "/w/q")
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := sessions.CreateManaged(ctx, "trainer-cd34", "", "/w/q")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.ManagedSession{{SessionID: managed.ID, OfferID: "O1"}, {SessionID: orphan.ID, OfferID: "gone"}} {
		if err := st.PutManaged(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	offers := offerGetter(func(_ context.Context, id string) (store.Offer, error) {
		if id == "O1" {
			return store.Offer{ID: "O1", IdleTimeout: 3 * time.Hour}, nil
		}
		return store.Offer{}, core.ErrNotFound
	})
	grace := presenceGrace(sessions, st, offers)
	for _, c := range []struct {
		session string
		want    time.Duration
	}{{live.Session.ID, core.AwayGrace}, {managed.ID, 3 * time.Hour}, {orphan.ID, core.DefaultIdleTimeout}, {"unknown", core.AwayGrace}} {
		if got := grace(ctx, store.Link{Session: c.session}); got != c.want {
			t.Errorf("session %s: grace %s, want %s", c.session, got, c.want)
		}
	}
}

type offerGetter func(ctx context.Context, id string) (store.Offer, error)

func (f offerGetter) Get(ctx context.Context, id string) (store.Offer, error) { return f(ctx, id) }

// setNoSend makes v's direct sends fail (its relay connection is down).
func (n *v2Net) setNoSend(v *v2Node, fail bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.noSend == nil {
		n.noSend = map[core.MachineID]bool{}
	}
	n.noSend[v.id] = fail
}

// This machine's own relay connection is down (its mailbox is not live and
// its pings cannot leave): however long that lasts, the silence says nothing
// about the peer, and no link is marked away or closed.
func TestPresenceOwnOutageDoesNotCloseLinks(t *testing.T) {
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	tickBoth(t, n, a, b)
	online := false
	a.presence.SetOnline(func() bool { return online })
	n.setNoSend(a, true)
	n.setDown(a, true)
	for elapsed := time.Duration(0); elapsed < core.PresenceTimeout+core.AwayGrace+5*time.Minute; elapsed += core.PresenceInterval {
		tickAlice(t, n, a, core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("a link closed or away while this machine was offline: %+v", got)
	}
	if sent := n.sent(core.KindLinkClosed); len(sent) != 0 {
		t.Fatalf("link.closed queued to a healthy peer: %d", len(sent))
	}
	// Back online: the peer answers and the link stays up.
	online = true
	n.setNoSend(a, false)
	n.setDown(a, false)
	for range 10 {
		tickBoth(t, n, a, b)
		n.clock.Advance(core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("after reconnecting: %+v", got)
	}
}

// A mailbox that reports live while every ping fails (a flapping
// connection) counts the same: silence after pings that never left is not
// evidence. Once the pings leave again, a silent peer does time out.
func TestPresencePingsThatDidNotLeaveDoNotCount(t *testing.T) {
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	tickBoth(t, n, a, b)
	n.setNoSend(a, true)
	n.setDown(b, true)
	for elapsed := time.Duration(0); elapsed < core.PresenceTimeout+core.AwayGrace+time.Minute; elapsed += core.PresenceInterval {
		tickAlice(t, n, a, core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkActive || !got.PresenceAway.IsZero() {
		t.Fatalf("silence after unsent pings timed the link out: %+v", got)
	}
	n.setNoSend(a, false)
	for elapsed := time.Duration(0); elapsed <= core.PresenceTimeout+core.AwayGrace+2*core.PresenceInterval; elapsed += core.PresenceInterval {
		tickAlice(t, n, a, core.PresenceInterval)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkClosed || got.Reason != core.ClosePresenceTimeout {
		t.Fatalf("a silent peer with pings leaving: %+v", got)
	}
}
