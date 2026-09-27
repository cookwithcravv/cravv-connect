package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

func discoveryPair(t *testing.T) (*v2Net, *v2Node, *v2Node) {
	t.Helper()
	n := newV2Net(t)
	a, b := n.node("alice"), n.node("bob")
	n.pair(a, b)
	return n, a, b
}

func listedNames(b core.SessionsListedBody) []string {
	var out []string
	for _, s := range b.Sessions {
		out = append(out, s.Name+":"+string(s.State))
	}
	return out
}

func TestDiscoveryListsOnlyVisibleSessions(t *testing.T) {
	ctx := context.Background()
	_, a, b := discoveryPair(t)
	shareOn(t, b, 1, "hidden", core.Visibility{})
	shareOn(t, b, 2, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	shareOn(t, b, 3, "for-alice", core.Visibility{Mode: core.VisibilityPeers, Peers: []core.MachineID{a.id}})
	shareOn(t, b, 4, "for-others", core.Visibility{Mode: core.VisibilityPeers, Peers: []core.MachineID{"someone-else"}})
	away := shareOn(t, b, 5, "sleepy", core.Visibility{Mode: core.VisibilityAllPeers})
	if err := b.shared.Detach(ctx, away.Session.ID, 5); err != nil {
		t.Fatal(err)
	}
	closed := shareOn(t, b, 6, "gone", core.Visibility{Mode: core.VisibilityAllPeers})
	if err := b.shared.Close(ctx, closed.Session.ID); err != nil {
		t.Fatal(err)
	}
	peer, got, err := a.discover.List(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if peer.MachineID != b.id {
		t.Fatalf("listed peer %s", peer.MachineID)
	}
	want := []string{"trainer:open", "for-alice:open", "sleepy:away"}
	if names := listedNames(got); len(names) != len(want) || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("alice sees %v, want %v", names, want)
	}
	for _, s := range got.Sessions {
		if s.Kind != core.SessionLive || s.Agent != "claude" || !core.ValidID(s.SessionID) {
			t.Fatalf("entry %+v", s)
		}
	}
	if got.Offers == nil || len(got.Offers) != 0 {
		t.Fatalf("offers = %#v, want an empty list", got.Offers)
	}
}

func TestDiscoveryRateLimitPerPeer(t *testing.T) {
	ctx := context.Background()
	n, a, _ := discoveryPair(t)
	a.discover.timeout = 50 * time.Millisecond
	for i := range core.DiscoveryPerMinute {
		if _, _, err := a.discover.List(ctx, "bob"); err != nil {
			t.Fatalf("list %d: %v", i+1, err)
		}
	}
	if _, _, err := a.discover.List(ctx, "bob"); !errors.Is(err, ErrDiscoveryTimeout) {
		t.Fatalf("list over the limit: err = %v, want no answer", err)
	}
	if got := len(n.sent(core.KindSessionsListed)); got != core.DiscoveryPerMinute {
		t.Fatalf("bob answered %d times", got)
	}
	n.clock.Advance(time.Minute)
	if _, _, err := a.discover.List(ctx, "bob"); err != nil {
		t.Fatalf("after a minute: %v", err)
	}
}

func TestDiscoveryDropsInvalidAndUnaskedAnswers(t *testing.T) {
	ctx := context.Background()
	_, a, b := discoveryPair(t)
	bobAtAlice := a.peerRec(b)
	// An answer nobody asked for is ignored.
	env, err := core.NewEnvelope(a.net.clock, b.id, a.id, core.KindSessionsListed, core.SessionsListedBody{ReqID: core.NewID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.discover.HandleListed(ctx, bobAtAlice, env); err != nil {
		t.Fatal(err)
	}
	// Entries with a bad ID, name, kind or state are dropped; a bad purpose or agent is blanked.
	good := core.ListedSession{SessionID: core.NewID(), Name: "ok", Purpose: "fine", Kind: core.SessionLive, Agent: "claude", State: core.SessionOpen}
	cases := []struct {
		in   core.ListedSession
		keep bool
	}{
		{good, true},
		{core.ListedSession{SessionID: "bad", Name: "ok", Kind: core.SessionLive, State: core.SessionOpen}, false},
		{core.ListedSession{SessionID: core.NewID(), Name: "Bad Name", Kind: core.SessionLive, State: core.SessionOpen}, false},
		{core.ListedSession{SessionID: core.NewID(), Name: "ok", Kind: "robot", State: core.SessionOpen}, false},
		{core.ListedSession{SessionID: core.NewID(), Name: "ok", Kind: core.SessionLive, State: core.SessionClosed}, false},
	}
	for _, c := range cases {
		got, ok := cleanListedSession(c.in)
		if ok != c.keep {
			t.Errorf("cleanListedSession(%+v) kept = %v", c.in, ok)
		}
		if ok && got.Name != c.in.Name {
			t.Errorf("cleaned %+v", got)
		}
	}
	blank, ok := cleanListedSession(core.ListedSession{SessionID: core.NewID(), Name: "ok", Purpose: "a\nb", Kind: core.SessionLive, Agent: "<script>", State: core.SessionAway})
	if !ok || blank.Purpose != "" || blank.Agent != "" {
		t.Fatalf("blanked = %+v, %v", blank, ok)
	}
}

func TestDiscoveryRefusesPausedPeers(t *testing.T) {
	ctx := context.Background()
	_, a, b := discoveryPair(t)
	p := a.peerRec(b)
	p.Paused = true
	if err := a.st.PutPeer(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.discover.List(ctx, "bob"); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("paused: %v", err)
	}
	p.Paused, p.PausedByPeer = false, true
	if err := a.st.PutPeer(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.discover.List(ctx, "bob"); !errors.Is(err, core.ErrPausedByPeer) {
		t.Fatalf("paused by peer: %v", err)
	}
	if _, _, err := a.discover.List(ctx, "nobody"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown machine: %v", err)
	}
}
