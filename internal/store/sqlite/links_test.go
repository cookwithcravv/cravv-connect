package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func linkFixture(peer core.MachineID, id string) store.Link {
	return store.Link{
		Peer: peer, ID: id, Direction: store.LinkInbound, Session: "S1", RemoteSession: "R1",
		RemoteName: "lead", RemotePurpose: "coordinates", PermissionIn: core.PermTasksAsk,
		Proposed: core.PermTasksAuto, Note: "please", State: store.LinkPending,
		CreatedAt: t0, UpdatedAt: t0, ExpiresAt: t0.Add(10 * time.Minute),
	}
}

func TestLinksInsertGetUpdate(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	a, err := db.InsertLink(ctx, linkFixture("m1", "L1"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.InsertLink(ctx, linkFixture("m2", "L1")) // same link_id from another machine
	if err != nil {
		t.Fatal(err)
	}
	if a.Num == 0 || b.Num <= a.Num {
		t.Fatalf("nums %d, %d: want increasing handles", a.Num, b.Num)
	}
	if _, err := db.InsertLink(ctx, linkFixture("m1", "L1")); !errors.Is(err, store.ErrLinkExists) {
		t.Fatalf("duplicate (peer, link_id): err = %v", err)
	}
	got, err := db.GetLink(ctx, "m1", "L1")
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("GetLink = %+v, %v\nwant %+v", got, err, a)
	}
	byNum, err := db.GetLinkByNum(ctx, b.Num)
	if err != nil || byNum.Peer != "m2" {
		t.Fatalf("GetLinkByNum = %+v, %v", byNum, err)
	}
	if _, err := db.GetLink(ctx, "m3", "L1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	up, err := db.UpdateLink(ctx, "m1", "L1", func(l *store.Link) error {
		l.State = store.LinkActive
		l.PermissionOut = core.PermMessages
		l.RemoteAway = true
		l.ExpiresAt = time.Time{}
		l.Num = 999 // ignored: the handle never changes
		l.Peer = "other"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Num != a.Num || up.Peer != "m1" || up.State != store.LinkActive || !up.RemoteAway {
		t.Fatalf("UpdateLink = %+v", up)
	}
	got, _ = db.GetLink(ctx, "m1", "L1")
	if got.Num != a.Num || got.PermissionOut != core.PermMessages || !got.ExpiresAt.IsZero() {
		t.Fatalf("after update = %+v", got)
	}
	boom := errors.New("boom")
	if _, err := db.UpdateLink(ctx, "m1", "L1", func(*store.Link) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("mutate error = %v", err)
	}
	if _, err := db.UpdateLink(ctx, "m9", "L1", func(*store.Link) error { return nil }); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("update missing err = %v", err)
	}
}

func TestListLinksFilters(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mk := func(peer core.MachineID, id, session string, dir store.LinkDirection, st store.LinkState, exp time.Time) {
		l := linkFixture(peer, id)
		l.Session, l.Direction, l.State, l.ExpiresAt = session, dir, st, exp
		if _, err := db.InsertLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	mk("m1", "A", "S1", store.LinkInbound, store.LinkPending, t0.Add(time.Minute))
	mk("m1", "B", "S1", store.LinkOutbound, store.LinkActive, time.Time{})
	mk("m2", "C", "S2", store.LinkInbound, store.LinkClosed, time.Time{})
	mk("m2", "D", "S2", store.LinkInbound, store.LinkPending, t0.Add(time.Hour))
	ids := func(f store.LinkFilter) []string {
		ls, err := db.ListLinks(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range ls {
			out = append(out, l.ID)
		}
		return out
	}
	cases := []struct {
		f    store.LinkFilter
		want []string
	}{
		{store.LinkFilter{}, []string{"A", "B", "C", "D"}},
		{store.LinkFilter{Peer: "m1"}, []string{"A", "B"}},
		{store.LinkFilter{Session: "S2"}, []string{"C", "D"}},
		{store.LinkFilter{States: []store.LinkState{store.LinkPending, store.LinkActive}}, []string{"A", "B", "D"}},
		{store.LinkFilter{Direction: store.LinkInbound, States: []store.LinkState{store.LinkPending}, Peer: "m2"}, []string{"D"}},
		{store.LinkFilter{ExpiredBefore: t0.Add(30 * time.Minute)}, []string{"A"}},
	}
	for _, c := range cases {
		if got := ids(c.f); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ListLinks(%+v) = %v, want %v", c.f, got, c.want)
		}
	}
}

func TestPurgeClosedLinks(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	open := linkFixture("m1", "A")
	closed := linkFixture("m1", "B")
	closed.State = store.LinkClosed
	for _, l := range []store.Link{open, closed} {
		if _, err := db.InsertLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.PurgeClosedLinks(ctx, t0.Add(time.Second))
	if err != nil || n != 1 {
		t.Fatalf("PurgeClosedLinks = %d, %v", n, err)
	}
	if _, err := db.GetLink(ctx, "m1", "A"); err != nil {
		t.Fatalf("open link purged: %v", err)
	}
}

// A link keeps when its peer machine stopped answering presence (zero
// while it answers), through insert, update and the upgrade to schema 9.
func TestLinkPresenceAwayRoundTrips(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	l := linkFixture("m1", "L1")
	l.State, l.PresenceAway = store.LinkActive, t0.Add(time.Minute)
	if _, err := db.InsertLink(ctx, l); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetLink(ctx, "m1", "L1"); err != nil || !got.PresenceAway.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after insert %v, %v", got.PresenceAway, err)
	}
	if _, err := db.UpdateLink(ctx, "m1", "L1", func(x *store.Link) error { x.PresenceAway = time.Time{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetLink(ctx, "m1", "L1"); !got.PresenceAway.IsZero() {
		t.Fatalf("after clearing %v", got.PresenceAway)
	}
}
