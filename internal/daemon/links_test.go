package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

func linkNet(t *testing.T) (*v2Net, *v2Node, *v2Node) {
	t.Helper()
	n := newV2Net(t)
	a, b := n.node("alice"), n.node("bob")
	n.pair(a, b)
	return n, a, b
}

func TestLinkRequestAndAccept(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermTasksAsk, "please train")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != store.LinkPending || out.Direction != store.LinkOutbound || out.PermissionIn != core.PermMessages ||
		out.RemoteSession != trainer.Session.ID || out.RemoteName != "trainer" {
		t.Fatalf("outgoing link %+v", out)
	}
	n.pump()
	in := b.linkOf(t, a, out.ID)
	if in.State != store.LinkPending || in.Direction != store.LinkInbound || in.Session != trainer.Session.ID ||
		in.Proposed != core.PermTasksAsk || in.Note != "please train" || in.RemoteName != "lead" || in.PermissionIn != "" {
		t.Fatalf("incoming request %+v", in)
	}
	items := b.notices(t, trainer.Session.ID)
	if len(items) != 1 || items[0].Kind != core.KindLinkRequest || items[0].LinkID != out.ID || items[0].FromSession != "lead" {
		t.Fatalf("trainer's inbox %+v", items)
	}
	if got := renderLinkNotice(items[0]).Text; !strings.Contains(got, "cravv-connect link accept") || !strings.Contains(got, "please train") {
		t.Fatalf("request notice %q", got)
	}
	if d := b.desktop.all(); len(d) != 1 || !strings.Contains(d[0], "link request") || !strings.Contains(d[0], "alice") {
		t.Fatalf("desktop %v", d)
	}
	if _, err := b.links.Decide(ctx, in.Num, true, "", AuthNone); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("accept without any human decision: %v", err)
	}
	acc, err := b.links.Decide(ctx, in.Num, true, "", AuthChat)
	if err != nil {
		t.Fatal(err)
	}
	if acc.State != store.LinkActive || acc.PermissionIn != core.PermTasksAsk || !acc.ExpiresAt.IsZero() {
		t.Fatalf("accepted %+v", acc)
	}
	// The requester lets the acceptor send messages only (its PermissionIn).
	if acc.PermissionOut != core.PermMessages {
		t.Fatalf("acceptor's PermissionOut %q, want messages", acc.PermissionOut)
	}
	n.pump()
	got := a.linkOf(t, b, out.ID)
	if got.State != store.LinkActive || got.PermissionOut != core.PermTasksAsk || got.PermissionIn != core.PermMessages {
		t.Fatalf("requester after accept %+v", got)
	}
	notes := a.notices(t, lead.Session.ID)
	if len(notes) != 1 || notes[0].Kind != core.KindLinkAccepted {
		t.Fatalf("lead's inbox %+v", notes)
	}
	if _, err := b.links.Decide(ctx, in.Num, true, "", AuthPassword); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("deciding twice: %v", err)
	}
}

// The tiered gate: a chat decision may accept at messages or tasks-ask,
// only the password may grant tasks-auto, and nobody grants more than asked.
func TestAcceptTiers(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	request := func(p core.Permission) store.Link {
		out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", p, "")
		if err != nil {
			t.Fatal(err)
		}
		n.pump()
		return b.linkOf(t, a, out.ID)
	}
	auto := request(core.PermTasksAuto)
	if _, err := b.links.Decide(ctx, auto.Num, true, core.PermTasksAuto, AuthChat); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("tasks-auto from a chat decision: %v", err)
	}
	if l, err := b.links.Decide(ctx, auto.Num, true, core.PermTasksAsk, AuthChat); err != nil || l.PermissionIn != core.PermTasksAsk {
		t.Fatalf("lowering at accept time: %+v, %v", l, err)
	}
	ask := request(core.PermMessages)
	if _, err := b.links.Decide(ctx, ask.Num, true, core.PermTasksAsk, AuthPassword); !errors.Is(err, ErrBadPermission) {
		t.Fatalf("granting more than asked: %v", err)
	}
	full := request(core.PermTasksAuto)
	if l, err := b.links.Decide(ctx, full.Num, true, "", AuthPassword); err != nil || l.PermissionIn != core.PermTasksAuto {
		t.Fatalf("password accept: %+v, %v", l, err)
	}
	n.pump()
	if got := a.linkOf(t, b, auto.ID); got.PermissionOut != core.PermTasksAsk {
		t.Fatalf("requester sees %s, want the lowered tasks-ask", got.PermissionOut)
	}
}

func TestDecideViaDecider(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermTasksAuto, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	in := b.linkOf(t, a, out.ID)
	if _, err := b.links.DecideVia(ctx, NoDecider{}, in.Num); !errors.Is(err, ErrNoDecision) {
		t.Fatalf("NoDecider: %v", err)
	}
	if got := b.linkOf(t, a, out.ID); got.State != store.LinkPending {
		t.Fatalf("no answer must leave the request pending, got %s", got.State)
	}
	if _, err := b.links.DecideVia(ctx, fixedDecider{DecisionAnswer{Accept: true}}, in.Num); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("a chat answer granted tasks-auto: %v", err)
	}
	l, err := b.links.DecideVia(ctx, fixedDecider{DecisionAnswer{Accept: true, Permission: core.PermTasksAsk}}, in.Num)
	if err != nil || l.PermissionIn != core.PermTasksAsk {
		t.Fatalf("chat accept at tasks-ask: %+v, %v", l, err)
	}
}

type fixedDecider struct{ ans DecisionAnswer }

func (f fixedDecider) Decide(context.Context, DecisionRequest) (DecisionAnswer, error) {
	return f.ans, nil
}

func TestRejectBusyAndDeclined(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	var outs []store.Link
	for range core.MaxPendingLinkRequests + 1 {
		out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermMessages, "")
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, out)
	}
	n.pump()
	last := a.linkOf(t, b, outs[len(outs)-1].ID)
	if last.State != store.LinkClosed || last.Reason != core.RejectBusy {
		t.Fatalf("sixth request %+v, want closed busy", last)
	}
	if _, err := b.st.GetLink(ctx, a.id, outs[len(outs)-1].ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("a busy request must not be stored")
	}
	first := b.linkOf(t, a, outs[0].ID)
	if _, err := b.links.Decide(ctx, first.Num, false, "", AuthNone); err != nil {
		t.Fatalf("rejecting needs no authority: %v", err)
	}
	n.pump()
	if got := a.linkOf(t, b, outs[0].ID); got.State != store.LinkClosed || got.Reason != core.RejectDeclined {
		t.Fatalf("declined request %+v", got)
	}
	notes := a.notices(t, lead.Session.ID)
	var rejected int
	for _, it := range notes {
		if it.Kind == core.KindLinkRejected {
			rejected++
		}
	}
	if rejected != 2 {
		t.Fatalf("lead got %d rejection notices, want 2 (busy, declined)", rejected)
	}
}

// A peer may send at most core.LinkRequestsPerMinute link.requests a
// minute, even when it keeps under the pending limit by cancelling or being
// declined. The excess is rejected busy and never reaches the session.
func TestLinkRequestRateLimited(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	request := func() string {
		t.Helper()
		id := core.NewID()
		body := core.LinkRequestBody{LinkID: id, FromSession: core.SessionRef{ID: core.NewID(), Name: "lead"},
			ToSessionID: trainer.Session.ID, ProposedPermission: core.PermMessages}
		if _, err := a.sender.SendEnvelope(ctx, b.id, core.KindLinkRequest, "", body); err != nil {
			t.Fatal(err)
		}
		n.pump()
		return id
	}
	for i := range core.LinkRequestsPerMinute {
		l := b.linkOf(t, a, request())
		if _, err := b.links.Decide(ctx, l.Num, false, "", AuthNone); err != nil {
			t.Fatalf("decline %d: %v", i, err)
		}
		n.pump()
	}
	requests := func() int {
		var c int
		for _, it := range b.notices(t, trainer.Session.ID) {
			if it.Kind == core.KindLinkRequest {
				c++
			}
		}
		return c
	}
	if got := requests(); got != core.LinkRequestsPerMinute {
		t.Fatalf("%d request notices, want %d", got, core.LinkRequestsPerMinute)
	}
	desk := len(b.desktop.all())
	over := request()
	if _, err := b.st.GetLink(ctx, a.id, over); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("a request over the rate must not be stored")
	}
	rej := n.sent(core.KindLinkRejected)
	if last := v2Body[core.LinkRejectedBody](t, rej[len(rej)-1]); last.LinkID != over || last.Reason != core.RejectBusy {
		t.Fatalf("over the rate: %+v, want busy", last)
	}
	if requests() != core.LinkRequestsPerMinute || len(b.desktop.all()) != desk {
		t.Fatal("a request over the rate notified the human")
	}
	n.clock.Advance(time.Minute)
	if l := b.linkOf(t, a, request()); l.State != store.LinkPending {
		t.Fatalf("after a minute %+v, want pending", l)
	}
}

// Review focus: a session the asker cannot see answers exactly like one
// that does not exist, so a peer cannot probe for private sessions.
func TestUnseenSessionLooksMissing(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	private := shareOn(t, b, 1, "secret", core.Visibility{})
	other := shareOn(t, b, 2, "not-for-alice", core.Visibility{Mode: core.VisibilityPeers, Peers: []core.MachineID{"someone"}})
	closed := shareOn(t, b, 3, "closed", core.Visibility{Mode: core.VisibilityAllPeers})
	if err := b.shared.Close(ctx, closed.Session.ID); err != nil {
		t.Fatal(err)
	}
	targets := []string{private.Session.ID, other.Session.ID, closed.Session.ID, core.NewID()}
	var ids []string
	for _, to := range targets {
		id := core.NewID()
		ids = append(ids, id)
		body := core.LinkRequestBody{LinkID: id, FromSession: core.SessionRef{ID: core.NewID(), Name: "prober"},
			ToSessionID: to, ProposedPermission: core.PermMessages}
		if _, err := a.sender.SendEnvelope(ctx, b.id, core.KindLinkRequest, "", body); err != nil {
			t.Fatal(err)
		}
	}
	n.pump()
	rejected := n.sent(core.KindLinkRejected)
	if len(rejected) != len(targets) {
		t.Fatalf("%d rejections for %d probes", len(rejected), len(targets))
	}
	for i, f := range rejected {
		got := string(f.env.Body)
		want := `{"link_id":"` + ids[i] + `","reason":"not_found"}`
		if got != want {
			t.Errorf("probe %d answered %s, want %s", i, got, want)
		}
		if _, err := b.st.GetLink(ctx, a.id, ids[i]); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("probe %d stored a link", i)
		}
	}
	for _, s := range []Shared{private, other} {
		if items := b.notices(t, s.Session.ID); len(items) != 0 {
			t.Errorf("%s was told about a probe: %+v", s.Session.Name, items)
		}
	}
}

func TestRequestTimeouts(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	// A request the relay held longer than the expiry is rejected on arrival.
	old := core.NewFakeClock(n.clock.Now().Add(-core.LinkRequestExpiry - time.Second))
	env, err := core.NewEnvelope(old, a.id, b.id, core.KindLinkRequest, core.LinkRequestBody{LinkID: core.NewID(),
		FromSession: core.SessionRef{ID: lead.Session.ID, Name: "lead"}, ToSessionID: trainer.Session.ID, ProposedPermission: core.PermMessages})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.links.HandleRequest(ctx, b.peerRec(a), env); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if r := n.sent(core.KindLinkRejected); len(r) != 1 || v2Body[core.LinkRejectedBody](t, r[0]).Reason != core.RejectTimeout {
		t.Fatalf("stale request answered %+v", r)
	}
	// A request nobody decides expires on both sides.
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	n.clock.Advance(core.LinkRequestExpiry)
	if k, err := b.links.ExpireDue(ctx); err != nil || k != 1 {
		t.Fatalf("receiver ExpireDue = %d, %v", k, err)
	}
	if k, err := a.links.ExpireDue(ctx); err != nil || k != 1 {
		t.Fatalf("requester ExpireDue = %d, %v", k, err)
	}
	if got := b.linkOf(t, a, out.ID); got.State != store.LinkClosed || got.Reason != core.RejectTimeout {
		t.Fatalf("receiver %+v", got)
	}
	n.pump()
	if got := a.linkOf(t, b, out.ID); got.State != store.LinkClosed || got.Reason != core.RejectTimeout {
		t.Fatalf("requester %+v", got)
	}
	if _, err := b.links.Decide(ctx, b.linkOf(t, a, out.ID).Num, true, "", AuthPassword); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("accepting an expired request: %v", err)
	}
}

func TestDisconnectClosesBothSides(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermTasksAsk)
	if err := a.links.Disconnect(ctx, "SOMEONE-ELSE", l.aLink.Num); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session's link: %v", err)
	}
	if err := a.links.Disconnect(ctx, l.lead.Session.ID, l.aLink.Num); err != nil {
		t.Fatal(err)
	}
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkClosed || got.Reason != ReasonDisconnected {
		t.Fatalf("local side %+v", got)
	}
	n.pump()
	got := b.linkOf(t, a, l.aLink.ID)
	if got.State != store.LinkClosed || got.Reason != core.CloseClosedByPeer {
		t.Fatalf("remote side %+v", got)
	}
	if len(a.closed) != 1 || len(b.closed) != 1 {
		t.Fatalf("close observers ran %d and %d times", len(a.closed), len(b.closed))
	}
	items := b.notices(t, l.worker.Session.ID)
	if last := items[len(items)-1]; last.Kind != core.KindLinkClosed || !strings.Contains(renderLinkNotice(last).Text, "closed_by_peer") {
		t.Fatalf("trainer's last notice %+v", last)
	}
	if _, err := a.links.Active(ctx, l.lead.Session.ID, l.aLink.Num); !errors.Is(err, core.ErrLinkClosed) {
		t.Fatalf("sending on a closed link: %v", err)
	}
}

func TestSessionCloseClosesLinks(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	// A second request to trainer is still pending when trainer closes.
	pending, err := a.links.Connect(ctx, l.lead.Session.ID, "bob/trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	if err := b.shared.Close(ctx, l.worker.Session.ID); err != nil {
		t.Fatal(err)
	}
	if got := b.linkOf(t, a, l.bLink.ID); got.State != store.LinkClosed || got.Reason != core.CloseSessionClosed {
		t.Fatalf("closing side %+v", got)
	}
	n.pump()
	if got := a.linkOf(t, b, l.aLink.ID); got.State != store.LinkClosed || got.Reason != core.CloseSessionClosed {
		t.Fatalf("peer side %+v", got)
	}
	if got := a.linkOf(t, b, pending.ID); got.State != store.LinkClosed || got.Reason != core.RejectNotFound {
		t.Fatalf("pending request %+v, want rejected not_found", got)
	}
}

// Review focus: a link.closed for a link this side does not know is never
// answered, so two sides that both lost a link cannot bounce replies;
// link.state and link.accepted on an unknown link get one rate-limited
// link.closed{unknown_link}.
func TestUnknownLinkReplies(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	ghost := core.NewID()
	send := func(kind core.Kind, body any) {
		if _, err := b.sender.SendEnvelope(ctx, a.id, kind, "", body); err != nil {
			t.Fatal(err)
		}
		n.pump()
	}
	send(core.KindLinkClosed, core.LinkClosedBody{LinkID: ghost, Reason: core.CloseUnknownLink})
	send(core.KindLinkClosed, core.LinkClosedBody{LinkID: ghost, Reason: core.CloseClosedByPeer})
	if got := len(n.sent(core.KindLinkClosed)); got != 2 {
		t.Fatalf("%d link.closed frames, want only the 2 sent (no answers)", got)
	}
	send(core.KindLinkState, core.LinkStateBody{LinkID: ghost, State: core.LinkStateActive, PermissionIn: core.PermMessages})
	send(core.KindLinkState, core.LinkStateBody{LinkID: ghost, State: core.LinkStateAway, PermissionIn: core.PermMessages})
	send(core.KindLinkAccepted, core.LinkAcceptedBody{LinkID: ghost, ToSession: core.SessionRef{ID: core.NewID(), Name: "x"}, GrantedPermission: core.PermMessages})
	closes := n.sent(core.KindLinkClosed)
	if len(closes) != 3 || closes[2].from != a.id || v2Body[core.LinkClosedBody](t, closes[2]).Reason != core.CloseUnknownLink {
		t.Fatalf("want exactly one unknown_link answer per minute, got %d link.closed", len(closes))
	}
	n.clock.Advance(core.UnknownLinkReplyEvery)
	send(core.KindLinkState, core.LinkStateBody{LinkID: ghost, State: core.LinkStateActive, PermissionIn: core.PermMessages})
	if got := len(n.sent(core.KindLinkClosed)); got != 4 {
		t.Fatalf("after a minute: %d link.closed, want 4", got)
	}
}

// A link.state for a link still pending here (it can overtake the
// link.accepted) is dropped silently: no unknown_link reply, no change.
func TestLinkStateOnPendingLinkIgnored(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	before := len(n.sent(core.KindLinkClosed))
	if _, err := b.sender.SendEnvelope(ctx, a.id, core.KindLinkState, "", core.LinkStateBody{LinkID: out.ID, State: core.LinkStateAway, PermissionIn: core.PermTasksAuto}); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if got := len(n.sent(core.KindLinkClosed)); got != before {
		t.Fatalf("link.state on a pending link got %d link.closed replies", got-before)
	}
	if got := a.linkOf(t, b, out.ID); got.State != store.LinkPending || got.RemoteAway || got.PermissionOut != "" {
		t.Fatalf("pending link changed: %+v", got)
	}
}

func TestSetPermission(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermTasksAuto)
	if _, err := b.links.SetPermission(ctx, l.worker.Session.ID, l.bLink.Num, core.PermMessages, AuthNone); err != nil {
		t.Fatalf("lowering needs no authority: %v", err)
	}
	if len(b.lowered) != 1 || b.lowered[0].PermissionIn != core.PermMessages {
		t.Fatalf("lower observers %+v", b.lowered)
	}
	n.pump()
	if got := a.linkOf(t, b, l.aLink.ID); got.PermissionOut != core.PermMessages {
		t.Fatalf("the peer learned %s", got.PermissionOut)
	}
	if _, err := b.links.SetPermission(ctx, l.worker.Session.ID, l.bLink.Num, core.PermTasksAsk, AuthChat); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("raising from chat: %v", err)
	}
	if got, err := b.links.SetPermission(ctx, "", l.bLink.Num, core.PermTasksAsk, AuthPassword); err != nil || got.PermissionIn != core.PermTasksAsk {
		t.Fatalf("raising with the password: %+v, %v", got, err)
	}
	if len(b.lowered) != 1 {
		t.Fatal("raising must not run the lower observers")
	}
	if _, err := a.links.SetPermission(ctx, l.worker.Session.ID, l.aLink.Num, core.PermMessages, AuthNone); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session's link: %v", err)
	}
}

func TestAwayAndBackTellPeers(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	if err := b.shared.Detach(ctx, l.worker.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if got := a.linkOf(t, b, l.aLink.ID); !got.RemoteAway || got.State != store.LinkActive {
		t.Fatalf("peer after away %+v", got)
	}
	if _, err := b.shared.Reattach(ctx, 7, l.worker.ReattachToken, "claude", "/p"); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if got := a.linkOf(t, b, l.aLink.ID); got.RemoteAway {
		t.Fatalf("peer after reattach %+v", got)
	}
}

func TestPeerCutOffAndKillCloseLinks(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	l := linkUp(t, n, a, b, core.PermMessages)
	if err := b.links.PeerCutOff(ctx, b.peerRec(a), CutOffPaused); err != nil {
		t.Fatal(err)
	}
	if got := b.linkOf(t, a, l.bLink.ID); got.State != store.LinkClosed || got.Reason != core.ClosePaused {
		t.Fatalf("after pause %+v", got)
	}
	if got := len(n.sent(core.KindLinkClosed)); got != 0 {
		t.Fatalf("a cut-off sent %d link.closed (control.paused tells the peer)", got)
	}
	// Kill closes the rest and tells the peers.
	pending, err := a.links.Connect(ctx, l.lead.Session.ID, "bob/trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	if err := b.links.CloseAll(ctx, core.CloseKilled); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if got := a.linkOf(t, b, pending.ID); got.State != store.LinkClosed || got.Reason != core.RejectDeclined {
		t.Fatalf("pending request at kill %+v", got)
	}
}

// link.decide acts only within the caller's session: another session's
// request looks missing and stays pending; the human's CLI ("") may decide
// any.
func TestDecideForIsScopedToTheSession(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	other := shareOn(t, b, 2, "other", core.Visibility{})
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	in := b.linkOf(t, a, out.ID)
	if _, err := b.links.DecideFor(ctx, other.Session.ID, in.Num, false, "", AuthNone); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session rejected the request: %v", err)
	}
	if got := b.linkOf(t, a, out.ID); got.State != store.LinkPending {
		t.Fatalf("state %s, want pending", got.State)
	}
	if l, err := b.links.DecideFor(ctx, trainer.Session.ID, in.Num, false, "", AuthNone); err != nil || l.State != store.LinkClosed {
		t.Fatalf("the owning session rejects: %+v, %v", l, err)
	}
	out2, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	if l, err := b.links.DecideFor(ctx, "", b.linkOf(t, a, out2.ID).Num, true, "", AuthPassword); err != nil || l.State != store.LinkActive {
		t.Fatalf("the human's CLI accepts: %+v, %v", l, err)
	}
}
