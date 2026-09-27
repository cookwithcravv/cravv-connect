package relayserver

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

func TestMemoryCreateRoomTreatsExpiredAsFree(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "ABCD", Owner: "a", ExpiresAt: clock.Now().Add(time.Minute)}, 8); err != nil {
		t.Fatal(err)
	}
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "ABCD", Owner: "b", ExpiresAt: clock.Now().Add(time.Minute)}, 8); !errors.Is(err, ErrExists) {
		t.Fatalf("live nameplate reused: %v", err)
	}
	clock.Advance(time.Minute)
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "ABCD", Owner: "b", ExpiresAt: clock.Now().Add(time.Minute)}, 8); err != nil {
		t.Fatalf("expired nameplate not reusable: %v", err)
	}
	if r, _ := be.GetRoom(ctx, "ABCD"); r.Owner != "b" || r.Joined {
		t.Fatalf("room = %+v", r)
	}
}

func TestMemoryRoomsPerOwnerCap(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	exp := clock.Now().Add(time.Minute)
	for _, np := range []string{"A000", "A001"} {
		if err := be.CreateRoom(ctx, RoomRecord{Nameplate: np, Owner: "o", ExpiresAt: exp}, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "A002", Owner: "o", ExpiresAt: exp}, 2); !errors.Is(err, ErrLimit) {
		t.Fatalf("third room err = %v, want ErrLimit", err)
	}
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "A002", Owner: "other", ExpiresAt: exp}, 2); err != nil {
		t.Fatalf("cap is per owner: %v", err)
	}
	_ = be.DeleteRoom(ctx, "A000")
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "A003", Owner: "o", ExpiresAt: exp}, 2); err != nil {
		t.Fatalf("after delete: %v", err)
	}
	clock.Advance(time.Minute)
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "A004", Owner: "o", ExpiresAt: clock.Now().Add(time.Minute)}, 2); err != nil {
		t.Fatalf("expired rooms still counted: %v", err)
	}
}

func TestMemoryInvitesCapAndRegister(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	for _, tok := range []string{"i1", "i2"} {
		if err := be.PutInvite(ctx, tok, "alice", time.Minute, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := be.PutInvite(ctx, "i3", "alice", time.Minute, 2); !errors.Is(err, ErrLimit) {
		t.Fatalf("third invite err = %v, want ErrLimit", err)
	}
	if err := be.PutInvite(ctx, "b1", "bob", time.Minute, 2); err != nil {
		t.Fatalf("cap is per inviter: %v", err)
	}
	ok, err := be.RegisterWithInvite(ctx, "i1", "newbie")
	if err != nil || !ok {
		t.Fatalf("register = %v %v", ok, err)
	}
	if m, _ := be.IsMember(ctx, "newbie"); !m {
		t.Fatal("RegisterWithInvite did not add the member")
	}
	if ok, _ := be.RegisterWithInvite(ctx, "i1", "second"); ok {
		t.Fatal("invite used twice")
	}
	if m, _ := be.IsMember(ctx, "second"); m {
		t.Fatal("refused registration added a member")
	}
	if err := be.PutInvite(ctx, "i3", "alice", time.Minute, 2); err != nil {
		t.Fatalf("consumed invite still counted: %v", err)
	}
	clock.Advance(time.Minute)
	if ok, _ := be.RegisterWithInvite(ctx, "i2", "late"); ok {
		t.Fatal("expired invite accepted")
	}
	if err := be.PutInvite(ctx, "i4", "alice", time.Minute, 2); err != nil {
		t.Fatalf("expired invites still counted: %v", err)
	}
}

func TestMemoryCreateBlobQuotas(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	exp := clock.Now().Add(time.Hour)
	if err := be.CreateBlob(ctx, BlobRecord{ID: "a", Uploader: "u", Size: 60, ExpiresAt: exp}, 100, 150); err != nil {
		t.Fatal(err)
	}
	if err := be.CreateBlob(ctx, BlobRecord{ID: "b", Uploader: "u", Size: 41, ExpiresAt: exp}, 100, 150); !errors.Is(err, ErrQuota) {
		t.Fatalf("per-member over quota err = %v, want ErrQuota", err)
	}
	if err := be.CreateBlob(ctx, BlobRecord{ID: "c", Uploader: "v", Size: 91, ExpiresAt: exp}, 100, 150); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("relay-wide over total err = %v, want ErrStorageFull", err)
	}
	if err := be.CreateBlob(ctx, BlobRecord{ID: "d", Uploader: "v", Size: 90, ExpiresAt: exp}, 100, 150+90); err != nil {
		t.Fatalf("within both caps: %v", err)
	}
	clock.Advance(time.Hour)
	if err := be.CreateBlob(ctx, BlobRecord{ID: "e", Uploader: "u", Size: 100, ExpiresAt: clock.Now().Add(time.Hour)}, 100, 150); err != nil {
		t.Fatalf("expired blobs still counted: %v", err)
	}
}

// The quota check and the insert are one atomic step: concurrent creates cannot overshoot.
func TestMemoryCreateBlobQuotaAtomic(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := BlobRecord{ID: string(rune('A' + i)), Uploader: "u", Size: 10, ExpiresAt: clock.Now().Add(time.Hour)}
			if be.CreateBlob(ctx, rec, 100, 1<<40) == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 10 {
		t.Fatalf("%d creates succeeded, want exactly 10", ok.Load())
	}
}

func TestMemoryPurgeExpired(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock).(*memoryBackend)
	now := clock.Now()
	lim := QueueLimits{MaxBytes: 1 << 20, MaxFrames: 10, TTL: time.Hour}

	_ = be.PutInvite(ctx, "old-inv", "a", time.Minute, 20)
	_ = be.CreateRoom(ctx, RoomRecord{Nameplate: "OLD0", Owner: "a", ExpiresAt: now.Add(time.Minute)}, 8)
	_ = be.CreateBlob(ctx, BlobRecord{ID: "old-blob", Uploader: "a", Size: 5, Chunks: 1, ExpiresAt: now.Add(time.Minute)}, 100, 100)
	_ = be.PutChunk(ctx, "old-blob", 0, []byte("hello"), 100)
	_, _ = be.Enqueue(ctx, "mb", nil, "old", []byte("o"), lim)

	clock.Advance(59 * time.Minute)
	_ = be.PutInvite(ctx, "new-inv", "a", time.Hour, 20)
	_ = be.CreateRoom(ctx, RoomRecord{Nameplate: "NEW0", Owner: "a", ExpiresAt: clock.Now().Add(time.Hour)}, 8)
	_ = be.CreateBlob(ctx, BlobRecord{ID: "new-blob", Uploader: "a", Size: 5, Chunks: 1, ExpiresAt: clock.Now().Add(time.Hour)}, 100, 100)
	_, _ = be.Enqueue(ctx, "mb", nil, "new", []byte("n"), lim)

	if err := be.PurgeExpired(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	be.mu.Lock()
	_, oldInv := be.invites["old-inv"]
	_, newInv := be.invites["new-inv"]
	_, oldRoom := be.rooms["OLD0"]
	_, newRoom := be.rooms["NEW0"]
	_, oldBlob := be.blobs["old-blob"]
	_, newBlob := be.blobs["new-blob"]
	q := be.queues["mb"]
	frames, qbytes, lastSeq := len(q.frames), q.bytes, q.lastSeq
	be.mu.Unlock()
	if oldInv || oldRoom || oldBlob {
		t.Fatalf("expired state kept: invite %v room %v blob %v", oldInv, oldRoom, oldBlob)
	}
	if !newInv || !newRoom || !newBlob {
		t.Fatalf("live state purged: invite %v room %v blob %v", newInv, newRoom, newBlob)
	}
	if frames != 1 || qbytes != 1 {
		t.Fatalf("queue after purge: %d frames %d bytes, want 1 and 1", frames, qbytes)
	}
	if seq, _ := be.Enqueue(ctx, "mb", nil, "next", []byte("x"), lim); seq != lastSeq+1 || lastSeq != 2 {
		t.Fatalf("seq after purge = %d (last %d), want 3", seq, lastSeq)
	}
}

func TestRateLimiterEvictsFullBuckets(t *testing.T) {
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	l := newRateLimiter(clock, 10, 5)
	l.allow("idle")
	l.allow("busy")
	for range 4 {
		l.allow("busy")
	}
	clock.Advance(100 * time.Millisecond) // idle refills to full, busy gets 1 of 5 back
	l.evictFull()
	l.mu.Lock()
	_, idle := l.buckets["idle"]
	_, busy := l.buckets["busy"]
	l.mu.Unlock()
	if idle || !busy {
		t.Fatalf("after evict: idle kept %v, busy kept %v", idle, busy)
	}
	// An evicted key starts again with a full bucket.
	for i := range 5 {
		if !l.allow("idle") {
			t.Fatalf("request %d refused after eviction", i)
		}
	}
}
