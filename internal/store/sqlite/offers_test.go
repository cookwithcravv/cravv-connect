package sqlite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func offerFixture(id string, peer core.MachineID, label string) store.Offer {
	return store.Offer{
		ID: id, Peer: peer, Label: label, Folder: "/srv/work/" + label, RealFolder: "/private/srv/work/" + label,
		Agent: "claude", Permission: core.PermTasksAuto, RunMode: core.RunEditInFolder, MaxConcurrent: 2,
		IdleTimeout: 2 * time.Hour, MaxTurnsPerRun: 40, RunTimeout: 30 * time.Minute, RunsPerHour: 30, RunsPerDay: 200,
		CreatedAt: t0, UpdatedAt: t0.Add(time.Minute),
	}
}

func TestOffersCRUD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	a := offerFixture("O1", "m1", "trainer")
	b := offerFixture("O2", "m1", "eval")
	c := offerFixture("O3", "m2", "trainer") // the same label for another machine is fine
	for _, o := range []store.Offer{a, b, c} {
		if err := db.PutOffer(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.GetOffer(ctx, "O1")
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("GetOffer = %+v, %v\nwant %+v", got, err, a)
	}
	if _, err := db.GetOffer(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing offer err = %v", err)
	}
	clash := offerFixture("O4", "m1", "trainer")
	if err := db.PutOffer(ctx, clash); !errors.Is(err, store.ErrOfferLabelTaken) {
		t.Fatalf("label clash err = %v", err)
	}
	a.RunMode, a.RunsPerHour = core.RunShell, 5
	if err := db.PutOffer(ctx, a); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := db.GetOffer(ctx, "O1"); got.RunMode != core.RunShell || got.RunsPerHour != 5 {
		t.Fatalf("update not stored: %+v", got)
	}
	m1, err := db.ListOffers(ctx, "m1")
	if err != nil || len(m1) != 2 || m1[0].ID != "O2" || m1[1].ID != "O1" {
		t.Fatalf("ListOffers(m1) = %+v, %v (want by label)", m1, err)
	}
	all, _ := db.ListOffers(ctx, "")
	if len(all) != 3 {
		t.Fatalf("ListOffers() = %d offers", len(all))
	}
	if err := db.DeleteOffer(ctx, "O2"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteOffer(ctx, "O2"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
}

func TestManagedSessionsFollowTheirSharedSession(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s := sharedFixture("S1", "trainer-ab12")
	s.Kind = core.SessionManaged
	if err := db.PutShared(ctx, s); err != nil {
		t.Fatal(err)
	}
	m := store.ManagedSession{
		SessionID: "S1", OfferID: "O1", Peer: "m1", LinkID: "L1", AgentSession: "0b5c2f6e-8a1d-4c3e-9f70-2d6a1b3c4d5e",
		LastActive: t0, CreatedAt: t0,
	}
	if err := db.PutManaged(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.Started, m.LastActive = true, t0.Add(time.Hour)
	if err := db.PutManaged(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetManaged(ctx, "S1")
	if err != nil || !reflect.DeepEqual(got, m) {
		t.Fatalf("GetManaged = %+v, %v\nwant %+v", got, err, m)
	}
	if list, _ := db.ListManaged(ctx); len(list) != 1 {
		t.Fatalf("ListManaged = %+v", list)
	}
	if err := db.PutManaged(ctx, store.ManagedSession{SessionID: "no-such-session"}); err == nil {
		t.Fatal("a managed record needs its shared session")
	}
	s.State, s.StateSince = core.SessionClosed, t0
	if err := db.PutShared(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PurgeClosedShared(ctx, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetManaged(ctx, "S1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("purging the session must drop its managed record: %v", err)
	}
}

func TestRunCounts(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	runs := []store.ManagedRun{
		{ID: "R1", SessionID: "S1", Peer: "m1", LinkID: "L1", StartedAt: t0},
		{ID: "R2", SessionID: "S1", Peer: "m1", LinkID: "L1", StartedAt: t0.Add(30 * time.Minute)},
		{ID: "R3", SessionID: "S2", Peer: "m1", LinkID: "L2", StartedAt: t0.Add(50 * time.Minute)},
		{ID: "R4", SessionID: "S3", Peer: "m2", LinkID: "L3", StartedAt: t0.Add(55 * time.Minute)},
	}
	for _, r := range runs {
		if err := db.AddRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		f    store.RunFilter
		want int
	}{
		{store.RunFilter{}, 4},
		{store.RunFilter{Peer: "m1"}, 3},
		{store.RunFilter{LinkID: "L1"}, 2},
		{store.RunFilter{LinkID: "L1", Since: t0.Add(time.Minute)}, 1},
		{store.RunFilter{Peer: "m1", Since: t0.Add(30 * time.Minute)}, 2},
	} {
		if n, err := db.CountRuns(ctx, c.f); err != nil || n != c.want {
			t.Errorf("CountRuns(%+v) = %d, %v; want %d", c.f, n, err, c.want)
		}
	}
	if n, err := db.PurgeRunsBefore(ctx, t0.Add(40*time.Minute)); err != nil || n != 2 {
		t.Fatalf("PurgeRunsBefore = %d, %v", n, err)
	}
	if n, _ := db.CountRuns(ctx, store.RunFilter{}); n != 2 {
		t.Fatalf("after purge %d runs", n)
	}
}

// AddRunCapped checks both caps and records the run in one transaction:
// concurrent starts can never exceed a cap.
func TestAddRunCapped(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	caps := store.RunCaps{PerLink: 2, LinkSince: t0, PerPeer: 3, PeerSince: t0}
	run := func(id, link string) store.ManagedRun {
		return store.ManagedRun{ID: id, SessionID: "S", Peer: "m1", LinkID: link, StartedAt: t0.Add(time.Minute)}
	}
	for i, c := range []struct {
		r    store.ManagedRun
		want store.RunCap
	}{
		{run("R1", "L1"), store.RunCapNone},
		{run("R2", "L1"), store.RunCapNone},
		{run("R3", "L1"), store.RunCapLink},
		{run("R4", "L2"), store.RunCapNone},
		{run("R5", "L3"), store.RunCapPeer},
	} {
		if got, err := db.AddRunCapped(ctx, c.r, caps); err != nil || got != c.want {
			t.Fatalf("%d: AddRunCapped = %v, %v; want %v", i, got, err, c.want)
		}
	}
	if n, _ := db.CountRuns(ctx, store.RunFilter{}); n != 3 {
		t.Fatalf("%d runs recorded, want 3", n)
	}
	// Old runs do not count.
	if got, err := db.AddRunCapped(ctx, run("R6", "L1"), store.RunCaps{PerLink: 1, LinkSince: t0.Add(2 * time.Minute), PerPeer: 9, PeerSince: t0}); err != nil || got != store.RunCapNone {
		t.Fatalf("a new window: %v, %v", got, err)
	}

	// Concurrent starts on one link: exactly the cap get through.
	fresh := newTestDB(t)
	var wg sync.WaitGroup
	var added atomic.Int32
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := fresh.AddRunCapped(ctx, run(fmt.Sprintf("C%d", i), "L1"), store.RunCaps{PerLink: 5, LinkSince: t0, PerPeer: 100, PeerSince: t0})
			if err != nil {
				t.Error(err)
			}
			if got == store.RunCapNone {
				added.Add(1)
			}
		}()
	}
	wg.Wait()
	if added.Load() != 5 {
		t.Fatalf("%d concurrent runs got through a cap of 5", added.Load())
	}
}
