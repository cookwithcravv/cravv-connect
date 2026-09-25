package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestPrekeyCurrentSupersedeDelete(t *testing.T) {
	ctx := context.Background()
	ps := newTestDB(t)
	if _, err := ps.CurrentPrekey(ctx); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("empty store CurrentPrekey err = %v, want ErrNotFound", err)
	}
	old := store.PrekeyRecord{ID: "pk1", Priv: []byte("priv1"), CreatedAt: t0}
	if err := ps.PutPrekey(ctx, old); err != nil {
		t.Fatal(err)
	}
	cur, err := ps.CurrentPrekey(ctx)
	if err != nil || cur.ID != "pk1" || string(cur.Priv) != "priv1" || cur.SupersededAt != nil {
		t.Fatalf("CurrentPrekey = %+v, %v", cur, err)
	}

	// Rotation: new key, supersede everything else.
	rot := t0.Add(core.PrekeyRotation)
	if err := ps.PutPrekey(ctx, store.PrekeyRecord{ID: "pk2", Priv: []byte("priv2"), CreatedAt: rot}); err != nil {
		t.Fatal(err)
	}
	if err := ps.SupersedeAllExcept(ctx, "pk2", rot); err != nil {
		t.Fatal(err)
	}
	cur, err = ps.CurrentPrekey(ctx)
	if err != nil || cur.ID != "pk2" {
		t.Fatalf("after rotation CurrentPrekey = %+v, %v", cur, err)
	}
	got, err := ps.GetPrekey(ctx, "pk1")
	if err != nil {
		t.Fatalf("superseded key must still be readable: %v", err)
	}
	if got.SupersededAt == nil || !got.SupersededAt.Equal(rot) {
		t.Fatalf("pk1 SupersededAt = %v, want %v", got.SupersededAt, rot)
	}

	// Superseding again must not move an existing supersede time.
	if err := ps.SupersedeAllExcept(ctx, "pk2", rot.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = ps.GetPrekey(ctx, "pk1")
	if !got.SupersededAt.Equal(rot) {
		t.Fatalf("supersede time moved to %v", got.SupersededAt)
	}

	// Retention: nothing deleted before the cutoff passes the supersede time.
	n, err := ps.DeleteSupersededBefore(ctx, rot)
	if err != nil || n != 0 {
		t.Fatalf("DeleteSupersededBefore(rot) = %d, %v; want 0", n, err)
	}
	n, err = ps.DeleteSupersededBefore(ctx, rot.Add(core.PrekeyRetention).Add(time.Millisecond))
	if err != nil || n != 1 {
		t.Fatalf("DeleteSupersededBefore(after retention) = %d, %v; want 1", n, err)
	}
	if _, err := ps.GetPrekey(ctx, "pk1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("pk1 after purge err = %v, want ErrNotFound", err)
	}
	// The current key is never deleted by the purge.
	if _, err := ps.GetPrekey(ctx, "pk2"); err != nil {
		t.Fatalf("current key purged: %v", err)
	}
}

func TestCurrentPrekeyPicksNewestUnsuperseded(t *testing.T) {
	ctx := context.Background()
	ps := newTestDB(t)
	sup := t0
	recs := []store.PrekeyRecord{
		{ID: "a", Priv: []byte("a"), CreatedAt: t0},
		{ID: "b", Priv: []byte("b"), CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "c", Priv: []byte("c"), CreatedAt: t0.Add(3 * time.Hour), SupersededAt: &sup},
	}
	for _, r := range recs {
		if err := ps.PutPrekey(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := ps.CurrentPrekey(ctx)
	if err != nil || cur.ID != "b" {
		t.Fatalf("CurrentPrekey = %q, %v; want b", cur.ID, err)
	}
}
