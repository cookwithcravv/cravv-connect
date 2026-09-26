package e2e

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// sessionNames lists the sessions as name:state, sorted: sessions shared
// in the same millisecond have no set order.
func sessionNames(r ipc.SessionsListResult) string {
	var names []string
	for _, s := range r.Sessions {
		names = append(names, s.Name+":"+s.State)
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

// Discovery shows only the sessions a machine may see; a session it cannot
// see is refused exactly like a missing one.
func TestShareAndDiscoverVisibility(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	lead := a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	secret := b.Share("codex", "secret", "private")

	var r ipc.SessionsListResult
	Call(t, a.Conn(), ipc.MethodSessionsList, ipc.MachineParams{Machine: "bob"}, &r)
	if got := sessionNames(r); got != "trainer:open" {
		t.Fatalf("alice sees %q, want only trainer", got)
	}
	if r.Sessions[0].Kind != "live" || !strings.Contains(r.Sessions[0].Wrapped, "trainer work") {
		t.Fatalf("entry %+v", r.Sessions[0])
	}
	wantKind(t, TryCall(lead.C, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "bob/secret", Permission: "messages"}, nil), ipc.KindNotFound)
	wantKind(t, TryCall(lead.C, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "bob/nothing", Permission: "messages"}, nil), ipc.KindNotFound)

	vis := "peers:alice"
	Call(t, secret.C, ipc.MethodSessionSet, ipc.SessionSetParams{Visibility: &vis}, nil)
	Call(t, a.Conn(), ipc.MethodSessionsList, ipc.MachineParams{Machine: "bob"}, &r)
	if got := sessionNames(r); got != "secret:open,trainer:open" {
		t.Fatalf("after session.set alice sees %q", got)
	}
	// Session names are unique among open sessions on a machine.
	c, _ := b.Session("claude")
	wantKind(t, TryCall(c, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "trainer"}, nil), ipc.KindBadRequest)
}

// A link request is decided once, on the accepting side; accepting needs
// the password in Phase 1. The requester sees the granted permission.
func TestLinkRequestAcceptedWithPassword(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	lead := a.Share("claude", "lead", "private")
	trainer := b.Share("claude", "trainer", "all-peers")
	out := Connect(t, lead, "bob/trainer", "tasks-ask", "please run the training job")
	if out.State != "pending" || out.Direction != "out" || out.RemoteSession != "trainer" {
		t.Fatalf("requester's link %+v", out)
	}
	in := b.WaitLink(wait, "request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	if in.Proposed != "tasks-ask" || in.Session != "trainer" || in.RemoteSession != "lead" || !strings.Contains(in.Wrapped, "please run the training job") {
		t.Fatalf("request as bob sees it %+v", in)
	}
	var n ipc.ListenResult
	n = b.Listen(trainer.Res.WakeToken, 5*time.Second)
	if n.Requests != 1 || n.Unread < 1 {
		t.Fatalf("listen counts %+v", n)
	}
	wantKind(t, TryCall(b.Conn(), ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: in.Link, Accept: true}, nil), ipc.KindAuthRequired)
	got := b.Decide(in.Link, true, "")
	if got.State != "active" || got.PermissionIn != "tasks-ask" {
		t.Fatalf("accepted %+v", got)
	}
	a.WaitLink(wait, "accepted", func(l ipc.LinkView) bool {
		return l.Link == out.Link && l.State == "active" && l.PermissionOut == "tasks-ask" && l.PermissionIn == "messages"
	})
	// Only the session's own links are visible to its connection.
	var mine ipc.LinksResult
	Call(t, trainer.C, ipc.MethodLinks, nil, &mine)
	if len(mine.Links) != 1 || mine.Links[0].Link != in.Link {
		t.Fatalf("trainer's links %+v", mine.Links)
	}
	other := b.Share("codex", "other", "private")
	Call(t, other.C, ipc.MethodLinks, nil, &mine)
	if len(mine.Links) != 0 {
		t.Fatalf("another session sees %+v", mine.Links)
	}
	wantKind(t, TryCall(other.C, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: in.Link}, nil), ipc.KindNotFound)
}

// An agent chat that has not shared a session, or has closed the one it
// shared, cannot see, restrict or disconnect another session's link. Only a
// human connection (no registered agent session) sees every link.
func TestUnsharedChatCannotReachOtherLinks(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "tasks-ask")
	unshared, _ := b.Session("codex")
	closed := b.Share("codex", "scratch", "private")
	Call(t, closed.C, ipc.MethodSessionClose, nil, nil)
	for what, c := range map[string]*ipc.Client{"unshared": unshared, "closed": closed.C} {
		wantKind(t, TryCall(c, ipc.MethodLinks, nil, nil), ipc.KindNotShared)
		wantKind(t, TryCall(c, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: l.BNum, Permission: "messages"}, nil), ipc.KindNotShared)
		wantKind(t, TryCall(c, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: l.BNum}, nil), ipc.KindNotShared)
		if got := b.Link(l.BNum); got.State != "active" || got.PermissionIn != "tasks-ask" {
			t.Fatalf("%s chat changed the link: %+v", what, got)
		}
	}
	if n := len(b.AllLinks()); n != 1 {
		t.Fatalf("the human sees %d links, want 1", n)
	}
}

func TestDisconnectClosesBothSides(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")
	Call(t, l.A.C, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: l.ANum}, nil)
	if got := a.Link(l.ANum); got.State != "closed" || got.Reason != "disconnected" {
		t.Fatalf("alice %+v", got)
	}
	b.WaitLink(wait, "closed by peer", func(v ipc.LinkView) bool {
		return v.Link == l.BNum && v.State == "closed" && v.Reason == core.CloseClosedByPeer
	})
}

// v2 success criterion 3: when a linked session closes, the other side
// learns within 5 seconds while both machines are online.
func TestSessionCloseReachesPeerWithinFiveSeconds(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")
	start := time.Now()
	Call(t, l.B.C, ipc.MethodSessionClose, nil, nil)
	a.WaitLink(5*time.Second, "session_closed", func(v ipc.LinkView) bool {
		return v.Link == l.ANum && v.State == "closed" && v.Reason == core.CloseSessionClosed
	})
	t.Logf("peer learned of the close after %s", time.Since(start).Round(time.Millisecond))
	wantKind(t, TryCall(l.B.C, ipc.MethodSessionClose, nil, nil), ipc.KindNotShared)
}

// v2 success criterion 3: when a machine drops, the link is away on the
// other side after the presence timeout (150 seconds, on a fake clock), and
// closes after the away grace.
func TestPresenceTimeoutWhenMachineDrops(t *testing.T) {
	t.Parallel()
	clock := core.NewFakeClock(time.Now())
	_, a, b := NewPairWithClock(t, clock)
	l := LinkUp(t, a, b, "messages")
	ctx := context.Background()
	// Bob drops first, so no pong from before the drop can arrive late.
	b.Stop()
	if err := a.Daemon.Presence().Tick(ctx); err != nil { // the link's timeout starts now
		t.Fatal(err)
	}
	for range 5 { // 150 seconds of pings nobody answers
		clock.Advance(core.PresenceInterval)
		if err := a.Daemon.Presence().Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Link(l.ANum); got.State != "active" || got.Unreachable {
		t.Fatalf("away before the timeout: %+v", got)
	}
	clock.Advance(time.Second)
	if err := a.Daemon.Presence().Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := a.Link(l.ANum); got.State != "active" || !got.Unreachable || !got.RemoteAway {
		t.Fatalf("after the timeout: %+v", got)
	}
	for range int(core.AwayGrace/core.PresenceInterval) + 1 {
		clock.Advance(core.PresenceInterval)
		if err := a.Daemon.Presence().Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Link(l.ANum); got.State != "closed" || got.Reason != core.ClosePresenceTimeout {
		t.Fatalf("after the away grace: %+v", got)
	}
}

// A session whose connection drops is away: its links stay open, and a
// reattach with the token brings it back with the same links.
func TestAwayAndReattachKeepLinks(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")
	l.B.C.Close()
	a.WaitLink(wait, "peer away", func(v ipc.LinkView) bool { return v.Link == l.ANum && v.RemoteAway && v.State == "active" })
	back := b.Reattach("claude", l.B)
	a.WaitLink(wait, "peer back", func(v ipc.LinkView) bool { return v.Link == l.ANum && !v.RemoteAway && v.State == "active" })
	var mine ipc.LinksResult
	Call(t, back.C, ipc.MethodLinks, nil, &mine)
	if len(mine.Links) != 1 || mine.Links[0].State != "active" {
		t.Fatalf("links after reattach %+v", mine.Links)
	}
	// Another agent or folder cannot take the session.
	c, _ := b.Session("codex")
	wantKind(t, TryCall(c, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: l.B.Res.ReattachToken}, nil), ipc.KindNotFound)
}

// An away session closes when the away grace (10 minutes) runs out, and its
// links close with it.
func TestAwayGraceExpiryClosesLinks(t *testing.T) {
	t.Parallel()
	clock := core.NewFakeClock(time.Now())
	_, a, b := NewPairWithClock(t, clock)
	l := LinkUp(t, a, b, "messages")
	l.B.C.Close()
	a.WaitLink(wait, "peer away", func(v ipc.LinkView) bool { return v.Link == l.ANum && v.RemoteAway })
	clock.Advance(core.AwayGrace + time.Second)
	if err := b.Daemon.Maintain(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.WaitLink(wait, "session_closed", func(v ipc.LinkView) bool {
		return v.Link == l.ANum && v.State == "closed" && v.Reason == core.CloseSessionClosed
	})
	c, _ := b.Session("claude")
	wantKind(t, TryCall(c, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: l.B.Res.ReattachToken}, nil), ipc.KindNotFound)
}

// link.decide is scoped like the other link methods: an unshared agent gets
// not_shared, another shared session sees nothing to decide, the human's
// CLI connection decides any request.
func TestLinkDecideIsScoped(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	lead := a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	other := b.Share("codex", "other", "private")
	Connect(t, lead, "bob/trainer", "messages", "")
	in := b.WaitLink(wait, "request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	unshared, _ := b.Session("codex")
	wantKind(t, TryCall(unshared, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: in.Link}, nil), ipc.KindNotShared)
	wantKind(t, TryCall(other.C, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: in.Link}, nil), ipc.KindNotFound)
	if got := b.Link(in.Link); got.State != "pending" {
		t.Fatalf("state %s, want pending", got.State)
	}
	var v ipc.LinkView
	Call(t, b.Conn(), ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: in.Link}, &v)
	if v.State != "closed" {
		t.Fatalf("the human's reject: %+v", v)
	}
}

// The human closes a chat's session from the terminal, without a password:
// its links close and the peer learns it (session_closed).
func TestSessionCloseFromTheCLI(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")
	if r := b.RunCLI("", "session", "close", l.B.Name); r.Code != 0 || r.Stdout != "Closed trainer; its links closed too.\n" {
		t.Fatalf("session close: %+v", r)
	}
	a.WaitLink(wait, "session_closed at alice", func(v ipc.LinkView) bool {
		return v.Link == l.ANum && v.State == "closed" && v.Reason == core.CloseSessionClosed
	})
	wantKind(t, TryCall(l.B.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: l.BNum, Text: "x"}, nil), ipc.KindNotShared)
	if r := b.RunCLI("", "session", "close", "ghost"); r.Code == 0 {
		t.Fatalf("closing an unknown session: %+v", r)
	}
}
