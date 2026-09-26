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

func sharedFixture(id, name string) store.SharedSession {
	return store.SharedSession{
		ID: id, Name: name, Purpose: "train models", Kind: core.SessionLive, Agent: "claude",
		ProjectDir: "/home/u/proj", Visibility: core.Visibility{Mode: core.VisibilityPeers, Peers: []core.MachineID{"m1"}},
		State: core.SessionOpen, ReattachHash: "r-" + id, WakeHash: "w-" + id, Cursor: 3,
		CreatedAt: t0, StateSince: t0,
	}
}

func TestSharedSessionsCRUD(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	s := sharedFixture("S1", "trainer")
	if err := db.PutShared(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetShared(ctx, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("GetShared = %+v\nwant %+v", got, s)
	}
	if _, err := db.GetShared(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if err := db.SetSharedCursor(ctx, "S1", 9); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSharedCursor(ctx, "nope", 9); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("SetSharedCursor missing err = %v", err)
	}
	if got, _ := db.GetShared(ctx, "S1"); got.Cursor != 9 {
		t.Fatalf("cursor = %d", got.Cursor)
	}
	byWake, err := db.SharedByWakeHash(ctx, "w-S1")
	if err != nil || byWake.ID != "S1" {
		t.Fatalf("SharedByWakeHash = %+v, %v", byWake, err)
	}
	byRe, err := db.SharedByReattachHash(ctx, "r-S1")
	if err != nil || byRe.ID != "S1" {
		t.Fatalf("SharedByReattachHash = %+v, %v", byRe, err)
	}
	if _, err := db.SharedByWakeHash(ctx, ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("empty hash must never match: %v", err)
	}
}

func TestSharedSessionNameUniqueAmongLive(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := db.PutShared(ctx, sharedFixture("S1", "trainer")); err != nil {
		t.Fatal(err)
	}
	if err := db.PutShared(ctx, sharedFixture("S2", "trainer")); !errors.Is(err, store.ErrNameTaken) {
		t.Fatalf("second open session with the name: err = %v, want ErrNameTaken", err)
	}
	away := sharedFixture("S1", "trainer")
	away.State = core.SessionAway
	if err := db.PutShared(ctx, away); err != nil {
		t.Fatal(err)
	}
	if err := db.PutShared(ctx, sharedFixture("S2", "trainer")); !errors.Is(err, store.ErrNameTaken) {
		t.Fatalf("an away session keeps its name: err = %v", err)
	}
	closed := away
	closed.State = core.SessionClosed
	if err := db.PutShared(ctx, closed); err != nil {
		t.Fatal(err)
	}
	if err := db.PutShared(ctx, sharedFixture("S2", "trainer")); err != nil {
		t.Fatalf("a closed session frees its name: %v", err)
	}
	if _, err := db.SharedByWakeHash(ctx, "w-S1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("a closed session's tokens must not resolve: %v", err)
	}
}

func TestListAndPurgeShared(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	for i, st := range []core.SessionState{core.SessionOpen, core.SessionAway, core.SessionClosed} {
		s := sharedFixture(string(rune('A'+i)), string(rune('a'+i)))
		s.State = st
		s.CreatedAt = t0.Add(time.Duration(i) * time.Second)
		if err := db.PutShared(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	all, err := db.ListShared(ctx)
	if err != nil || len(all) != 3 || all[0].ID != "A" || all[2].ID != "C" {
		t.Fatalf("ListShared() = %+v, %v", all, err)
	}
	live, err := db.ListShared(ctx, core.SessionOpen, core.SessionAway)
	if err != nil || len(live) != 2 {
		t.Fatalf("ListShared(open, away) = %+v, %v", live, err)
	}
	n, err := db.PurgeClosedShared(ctx, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PurgeClosedShared = %d, %v", n, err)
	}
	if all, _ := db.ListShared(ctx); len(all) != 2 {
		t.Fatalf("after purge %d sessions", len(all))
	}
}
