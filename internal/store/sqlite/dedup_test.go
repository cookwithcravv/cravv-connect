package sqlite

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

func TestDedupSeenOrMark(t *testing.T) {
	ctx := context.Background()
	ds := newTestDB(t)
	seen, err := ds.SeenOrMark(ctx, "m1", t0)
	if err != nil || seen {
		t.Fatalf("first SeenOrMark = %v, %v; want false", seen, err)
	}
	seen, err = ds.SeenOrMark(ctx, "m1", t0.Add(time.Hour))
	if err != nil || !seen {
		t.Fatalf("second SeenOrMark = %v, %v; want true", seen, err)
	}
	if seen, _ := ds.SeenOrMark(ctx, "m2", t0); seen {
		t.Fatal("different id reported seen")
	}
}

func TestDedupConcurrentExactlyOneFirst(t *testing.T) {
	ctx := context.Background()
	ds := newTestDB(t)
	var firsts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen, err := ds.SeenOrMark(ctx, "dup", t0)
			if err != nil {
				t.Error(err)
				return
			}
			if !seen {
				firsts.Add(1)
			}
		}()
	}
	wg.Wait()
	if firsts.Load() != 1 {
		t.Fatalf("%d callers saw the id as new, want 1", firsts.Load())
	}
}

func TestDedupDeleteOlderThan(t *testing.T) {
	ctx := context.Background()
	ds := newTestDB(t)
	ds.SeenOrMark(ctx, "old", t0)
	ds.SeenOrMark(ctx, "new", t0.Add(core.DedupWindow))
	n, err := ds.PurgeDedupBefore(ctx, t0.Add(time.Millisecond))
	if err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v", n, err)
	}
	if seen, _ := ds.SeenOrMark(ctx, "old", t0); seen {
		t.Fatal("purged id still seen")
	}
	if seen, _ := ds.SeenOrMark(ctx, "new", t0); !seen {
		t.Fatal("kept id no longer seen")
	}
}

func TestDedupSeenDoesNotMark(t *testing.T) {
	ctx := context.Background()
	ds := newTestDB(t)
	seen, err := ds.Seen(ctx, "m1")
	if err != nil || seen {
		t.Fatalf("Seen on empty store = %v, %v", seen, err)
	}
	if seen, _ := ds.Seen(ctx, "m1"); seen {
		t.Fatal("Seen marked the id")
	}
	if seen, _ := ds.SeenOrMark(ctx, "m1", t0); seen {
		t.Fatal("SeenOrMark after Seen reported seen")
	}
	if seen, err := ds.Seen(ctx, "m1"); err != nil || !seen {
		t.Fatalf("Seen after mark = %v, %v", seen, err)
	}
}
