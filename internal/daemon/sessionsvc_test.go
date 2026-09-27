package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// sessionEvents records SessionObserver calls as "away:<name>" and so on.
type sessionEvents struct {
	mu  sync.Mutex
	evs []string
}

func (e *sessionEvents) add(kind string, s store.SharedSession) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evs = append(e.evs, kind+":"+s.Name)
}
func (e *sessionEvents) SessionAway(_ context.Context, s store.SharedSession)   { e.add("away", s) }
func (e *sessionEvents) SessionBack(_ context.Context, s store.SharedSession)   { e.add("back", s) }
func (e *sessionEvents) SessionClosed(_ context.Context, s store.SharedSession) { e.add("closed", s) }
func (e *sessionEvents) all() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.evs, ",")
}

func newSessionSvc(t *testing.T) (*SessionService, *core.FakeClock, *sessionEvents, store.Store) {
	t.Helper()
	st := d2Store(t)
	clock := core.NewFakeClock(d2Epoch)
	svc := NewSessionService(st, clock)
	ev := &sessionEvents{}
	svc.AddObserver(ev)
	return svc, clock, ev, st
}

func share(t *testing.T, svc *SessionService, conn uint64, name string) Shared {
	t.Helper()
	if fc, ok := svc.clock.(*core.FakeClock); ok {
		fc.Advance(time.Millisecond) // sessions list in creation order
	}
	sh, err := svc.Share(context.Background(), conn, ShareRequest{Agent: "claude", ProjectDir: "/p", Name: name, Purpose: "work"})
	if err != nil {
		t.Fatalf("share %s: %v", name, err)
	}
	return sh
}

func TestShareValidatesAndStoresOnlyHashes(t *testing.T) {
	ctx := context.Background()
	svc, _, _, st := newSessionSvc(t)
	bad := []ShareRequest{
		{Name: "Trainer"}, {Name: ""}, {Name: strings.Repeat("a", 33)},
		{Name: "ok", Purpose: "two\nlines"},
		{Name: "ok", Visibility: core.Visibility{Mode: core.VisibilityPeers}},
	}
	for _, r := range bad {
		if _, err := svc.Share(ctx, 1, r); err == nil {
			t.Errorf("Share(%+v) succeeded", r)
		}
	}
	sh := share(t, svc, 1, "trainer")
	if sh.WakeToken == "" || sh.ReattachToken == "" || sh.WakeToken == sh.ReattachToken {
		t.Fatalf("tokens %q %q", sh.WakeToken, sh.ReattachToken)
	}
	rec, err := st.GetShared(ctx, sh.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Visibility.Mode != core.VisibilityPrivate {
		t.Errorf("default visibility %q, want private", rec.Visibility.Mode)
	}
	if rec.WakeHash == sh.WakeToken || rec.ReattachHash == sh.ReattachToken ||
		rec.WakeHash != hashToken(sh.WakeToken) || rec.ReattachHash != hashToken(sh.ReattachToken) {
		t.Fatal("the store must hold token hashes, never the tokens")
	}
	if _, err := svc.Share(ctx, 1, ShareRequest{Name: "second"}); !errors.Is(err, ErrAlreadyShared) {
		t.Fatalf("second share on one connection: %v", err)
	}
	if _, err := svc.Share(ctx, 2, ShareRequest{Name: "trainer"}); !errors.Is(err, store.ErrNameTaken) {
		t.Fatalf("name clash: %v", err)
	}
	if got, err := svc.Current(ctx, sh.Session.ID, 1); err != nil || got.Name != "trainer" {
		t.Fatalf("Current = %+v, %v", got, err)
	}
	if _, err := svc.Current(ctx, sh.Session.ID, 2); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("Current on another connection: %v", err)
	}
}

func TestDetachGoesAwayAndReattachComesBack(t *testing.T) {
	ctx := context.Background()
	svc, _, ev, _ := newSessionSvc(t)
	sh := share(t, svc, 1, "trainer")
	if err := svc.Detach(ctx, sh.Session.ID, 99); err != nil { // not the bound connection
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, sh.Session.ID); got.State != core.SessionOpen {
		t.Fatalf("a stranger's disconnect changed the state to %s", got.State)
	}
	if err := svc.Detach(ctx, sh.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, sh.Session.ID); got.State != core.SessionAway {
		t.Fatalf("state after detach = %s", got.State)
	}
	for _, bad := range []struct{ token, agent, dir string }{
		{"wrong", "claude", "/p"}, {sh.ReattachToken, "codex", "/p"}, {sh.ReattachToken, "claude", "/other"},
		{sh.WakeToken, "claude", "/p"}, {"", "claude", "/p"},
	} {
		if _, err := svc.Reattach(ctx, 2, bad.token, bad.agent, bad.dir); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("Reattach(%+v) err = %v, want not found", bad, err)
		}
	}
	back, err := svc.Reattach(ctx, 2, sh.ReattachToken, "claude", "/p")
	if err != nil || back.State != core.SessionOpen {
		t.Fatalf("Reattach = %+v, %v", back, err)
	}
	if ev.all() != "away:trainer,back:trainer" {
		t.Fatalf("events %q", ev.all())
	}
}

// Review focus: a reattach takes the session over, and the connection that
// held it can no longer act as it, even before that connection closes.
func TestReattachTakeoverRevokesOldConnection(t *testing.T) {
	ctx := context.Background()
	svc, _, ev, _ := newSessionSvc(t)
	sh := share(t, svc, 1, "trainer")
	if _, err := svc.Reattach(ctx, 2, sh.ReattachToken, "claude", "/p"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Current(ctx, sh.Session.ID, 1); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("old connection still holds the session: %v", err)
	}
	if _, err := svc.Current(ctx, sh.Session.ID, 2); err != nil {
		t.Fatalf("new connection: %v", err)
	}
	// The old connection closing later must not send the session away.
	if err := svc.Detach(ctx, sh.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, sh.Session.ID); got.State != core.SessionOpen {
		t.Fatalf("state %s after the replaced connection closed", got.State)
	}
	if ev.all() != "" {
		t.Fatalf("a takeover of an open session is not a state change: %q", ev.all())
	}
	// A closed session can never be reattached.
	if err := svc.Close(ctx, sh.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Reattach(ctx, 3, sh.ReattachToken, "claude", "/p"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("reattach after close: %v", err)
	}
	if _, err := svc.ByWakeToken(ctx, sh.WakeToken); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("wake token after close: %v", err)
	}
}

// Review focus: a reattach that lands between Detach unbinding the old
// connection and marking the session away must win. The session stays open
// and bound to the new connection, and no away event is sent.
func TestReattachDuringDetachKeepsSessionOpen(t *testing.T) {
	ctx := context.Background()
	svc, _, ev, _ := newSessionSvc(t)
	sh := share(t, svc, 1, "trainer")
	svc.afterUnbind = func() {
		if _, err := svc.Reattach(ctx, 2, sh.ReattachToken, "claude", "/p"); err != nil {
			t.Errorf("reattach: %v", err)
		}
	}
	if err := svc.Detach(ctx, sh.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, sh.Session.ID); got.State != core.SessionOpen {
		t.Fatalf("state %s after a reattach raced the detach", got.State)
	}
	if _, err := svc.Current(ctx, sh.Session.ID, 2); err != nil {
		t.Fatalf("new connection lost the session: %v", err)
	}
	if ev.all() != "" {
		t.Fatalf("events %q, want none", ev.all())
	}
}

func TestAwayGraceSweepClosesAndFreesName(t *testing.T) {
	ctx := context.Background()
	svc, clock, ev, _ := newSessionSvc(t)
	sh := share(t, svc, 1, "trainer")
	if err := svc.Detach(ctx, sh.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(core.AwayGrace)
	if n, err := svc.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("sweep at exactly the grace = %d, %v", n, err)
	}
	clock.Advance(time.Second)
	if n, err := svc.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("sweep after the grace = %d, %v", n, err)
	}
	if got, _ := svc.Get(ctx, sh.Session.ID); got.State != core.SessionClosed {
		t.Fatalf("state %s", got.State)
	}
	if ev.all() != "away:trainer,closed:trainer" {
		t.Fatalf("events %q", ev.all())
	}
	share(t, svc, 2, "trainer") // the name is free again
}

func TestAwayAllAtStartupAndClose(t *testing.T) {
	ctx := context.Background()
	svc, _, ev, _ := newSessionSvc(t)
	a := share(t, svc, 1, "a")
	share(t, svc, 2, "b")
	if err := svc.Close(ctx, a.Session.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(ctx, a.Session.ID); err != nil { // closing twice is a no-op
		t.Fatal(err)
	}
	if err := svc.AwayAll(ctx); err != nil {
		t.Fatal(err)
	}
	if ev.all() != "closed:a,away:b" {
		t.Fatalf("events %q", ev.all())
	}
	if _, err := svc.Current(ctx, a.Session.ID, 1); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("closed session still current: %v", err)
	}
}

func TestVisibilityAndSet(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newSessionSvc(t)
	priv := share(t, svc, 1, "private-one")
	pub := share(t, svc, 2, "public-one")
	all := core.Visibility{Mode: core.VisibilityAllPeers}
	if _, err := svc.Set(ctx, pub.Session.ID, nil, &all); err != nil {
		t.Fatal(err)
	}
	only := core.Visibility{Mode: core.VisibilityPeers, Peers: []core.MachineID{"m2"}}
	purpose := "new purpose"
	got, err := svc.Set(ctx, priv.Session.ID, &purpose, &only)
	if err != nil || got.Purpose != purpose {
		t.Fatalf("Set = %+v, %v", got, err)
	}
	bad := core.Visibility{Mode: "public"}
	if _, err := svc.Set(ctx, priv.Session.ID, nil, &bad); !errors.Is(err, ErrBadVisibility) {
		t.Fatalf("bad visibility: %v", err)
	}
	names := func(peer core.MachineID) string {
		vs, err := svc.Visible(ctx, peer)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range vs {
			out = append(out, v.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("m1"); got != "public-one" {
		t.Errorf("m1 sees %q", got)
	}
	if got := names("m2"); got != "private-one,public-one" {
		t.Errorf("m2 sees %q", got)
	}
	if _, err := svc.VisibleTo(ctx, priv.Session.ID, "m1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("an unseen session must look missing: %v", err)
	}
	if _, err := svc.VisibleTo(ctx, "NOSUCHSESSION", "m1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing session: %v", err)
	}
}

// After Claude Code restarts, its chat shares the same name again from the
// same agent and folder: the away session is taken over (same session and
// links, new tokens) instead of refused. The old tokens stop working.
func TestShareTakesOverAwaySessionFromSameAgentAndFolder(t *testing.T) {
	ctx := context.Background()
	svc, _, ev, _ := newSessionSvc(t)
	old := share(t, svc, 1, "trainer")
	if err := svc.Detach(ctx, old.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Share(ctx, 2, ShareRequest{Agent: "claude", ProjectDir: "/p", Name: "trainer"})
	if err != nil {
		t.Fatalf("share again: %v", err)
	}
	if again.Session.ID != old.Session.ID || again.Session.State != core.SessionOpen || !again.Resumed {
		t.Fatalf("share again = %+v", again)
	}
	if again.Session.Purpose != "work" || again.Session.Visibility.Mode != core.VisibilityPrivate {
		t.Fatalf("a share without purpose or visibility changed them: %+v", again.Session)
	}
	if again.WakeToken == old.WakeToken || again.ReattachToken == old.ReattachToken {
		t.Fatal("the takeover kept the old tokens")
	}
	if _, err := svc.Current(ctx, old.Session.ID, 2); err != nil {
		t.Fatalf("not bound to the new chat: %v", err)
	}
	if _, err := svc.Reattach(ctx, 3, old.ReattachToken, "claude", "/p"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old reattach token: %v", err)
	}
	if _, err := svc.ByWakeToken(ctx, old.WakeToken); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old wake token: %v", err)
	}
	if ev.all() != "away:trainer,back:trainer" {
		t.Fatalf("events %q", ev.all())
	}

	// A takeover that names a purpose or visibility sets them.
	if err := svc.Detach(ctx, old.Session.ID, 2); err != nil {
		t.Fatal(err)
	}
	third, err := svc.Share(ctx, 3, ShareRequest{Agent: "claude", ProjectDir: "/p", Name: "trainer", Purpose: "new work",
		Visibility: core.Visibility{Mode: core.VisibilityAllPeers}})
	if err != nil || third.Session.Purpose != "new work" || third.Session.Visibility.Mode != core.VisibilityAllPeers {
		t.Fatalf("third share = %+v, %v", third.Session, err)
	}
}

// The name is refused when the session is open in another chat, or away
// but shared by another agent or from another folder, or managed.
func TestShareRefusesNameInUse(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _ := newSessionSvc(t)
	sh := share(t, svc, 1, "trainer")
	_, err := svc.Share(ctx, 2, ShareRequest{Agent: "claude", ProjectDir: "/p", Name: "trainer"})
	if !errors.Is(err, store.ErrNameTaken) || err.Error() != "a session named trainer is open in another chat; close it there or pick another name" {
		t.Fatalf("open elsewhere: %v", err)
	}
	if err := svc.Detach(ctx, sh.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, req := range []ShareRequest{{Agent: "codex", ProjectDir: "/p", Name: "trainer"}, {Agent: "claude", ProjectDir: "/other", Name: "trainer"}} {
		if _, err := svc.Share(ctx, 2, req); !errors.Is(err, store.ErrNameTaken) || !strings.Contains(err.Error(), "pick another name") {
			t.Fatalf("away, %+v: %v", req, err)
		}
	}
	if got, _ := svc.Get(ctx, sh.Session.ID); got.State != core.SessionAway {
		t.Fatalf("a refused share changed the session: %s", got.State)
	}
	if _, err := svc.CreateManaged(ctx, "helper-ab12", "", "/p"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Share(ctx, 3, ShareRequest{Agent: "claude", ProjectDir: "/p", Name: "helper-ab12"}); !errors.Is(err, store.ErrNameTaken) {
		t.Fatalf("managed name: %v", err)
	}
}
