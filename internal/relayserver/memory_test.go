package relayserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestMemoryQueueSeqNeverReused(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	lim := QueueLimits{MaxBytes: 1 << 20, MaxFrames: 10, TTL: time.Hour}
	from := ed25519.PublicKey(make([]byte, 32))
	for want := uint64(1); want <= 3; want++ {
		seq, err := be.Enqueue(ctx, "mb", from, "id", []byte("x"), lim)
		if err != nil || seq != want {
			t.Fatalf("enqueue = %d %v, want %d", seq, err, want)
		}
	}
	if err := be.Ack(ctx, "mb", 3); err != nil {
		t.Fatal(err)
	}
	if got, _ := be.Pending(ctx, "mb", 0, time.Hour, 10); len(got) != 0 {
		t.Fatalf("pending after ack = %d", len(got))
	}
	if seq, _ := be.Enqueue(ctx, "mb", from, "id", []byte("x"), lim); seq != 4 {
		t.Fatalf("seq after empty queue = %d, want 4", seq)
	}
}

func TestMemoryQueuePendingAfterAndLimit(t *testing.T) {
	ctx := context.Background()
	be := NewMemoryBackend(core.NewFakeClock(time.Unix(1_800_000_000, 0)))
	lim := QueueLimits{MaxBytes: 1 << 20, MaxFrames: 10, TTL: time.Hour}
	for range 5 {
		if _, err := be.Enqueue(ctx, "mb", nil, "id", []byte("x"), lim); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := be.Pending(ctx, "mb", 2, time.Hour, 2)
	if len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 4 {
		t.Fatalf("pending = %+v", got)
	}
}

func TestMemoryQueueExpiryFreesCapacity(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	lim := QueueLimits{MaxBytes: 1 << 20, MaxFrames: 1, TTL: time.Hour}
	if _, err := be.Enqueue(ctx, "mb", nil, "a", []byte("x"), lim); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Enqueue(ctx, "mb", nil, "b", []byte("x"), lim); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
	clock.Advance(time.Hour)
	if _, err := be.Enqueue(ctx, "mb", nil, "c", []byte("x"), lim); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestMemoryInvitesAndRooms(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(time.Unix(1_800_000_000, 0))
	be := NewMemoryBackend(clock)
	if err := be.PutInvite(ctx, "inv", time.Minute); err != nil {
		t.Fatal(err)
	}
	if ok, _ := be.ConsumeInvite(ctx, "inv"); !ok {
		t.Fatal("first consume failed")
	}
	if ok, _ := be.ConsumeInvite(ctx, "inv"); ok {
		t.Fatal("invite reused")
	}
	_ = be.PutInvite(ctx, "old", time.Minute)
	clock.Advance(time.Minute)
	if ok, _ := be.ConsumeInvite(ctx, "old"); ok {
		t.Fatal("expired invite accepted")
	}

	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "ABCD"}); err != nil {
		t.Fatal(err)
	}
	if err := be.CreateRoom(ctx, RoomRecord{Nameplate: "ABCD"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate nameplate err = %v", err)
	}
	if ok, _ := be.ClaimJoin(ctx, "ABCD"); !ok {
		t.Fatal("first join refused")
	}
	if ok, _ := be.ClaimJoin(ctx, "ABCD"); ok {
		t.Fatal("second join accepted")
	}
	_ = be.DeleteRoom(ctx, "ABCD")
	if _, err := be.GetRoom(ctx, "ABCD"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted room err = %v", err)
	}
}

func TestMemoryBlobTotalCap(t *testing.T) {
	ctx := context.Background()
	be := NewMemoryBackend(core.NewFakeClock(time.Unix(1_800_000_000, 0)))
	_ = be.CreateBlob(ctx, BlobRecord{ID: "b", Chunks: 2})
	if err := be.PutChunk(ctx, "b", 0, make([]byte, 6), 10); err != nil {
		t.Fatal(err)
	}
	if err := be.PutChunk(ctx, "b", 1, make([]byte, 5), 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	// Replacing a chunk counts only the new size.
	if err := be.PutChunk(ctx, "b", 0, make([]byte, 5), 10); err != nil {
		t.Fatal(err)
	}
	if err := be.PutChunk(ctx, "b", 1, make([]byte, 5), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := be.GetChunk(ctx, "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
