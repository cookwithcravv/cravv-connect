package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

func outItem(id string, to core.MachineID, created, next time.Time) store.OutboxItem {
	return store.OutboxItem{
		ID: id, To: to, Envelope: []byte(`{"id":"` + id + `"}`),
		Status: store.OutboxPending, NextAttempt: next, CreatedAt: created,
	}
}

func dueIDs(t *testing.T, os store.OutboxStore, now time.Time, limit int) []string {
	t.Helper()
	items, err := os.Due(context.Background(), now, limit)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOutboxDueOrderingAndNextAttempt(t *testing.T) {
	ctx := context.Background()
	ob := newTestDB(t)
	// Inserted out of order; Due must return oldest CreatedAt first.
	for _, it := range []store.OutboxItem{
		outItem("m3", "peer", t0.Add(3*time.Second), t0),
		outItem("m1", "peer", t0.Add(1*time.Second), t0),
		outItem("m2", "peer", t0.Add(2*time.Second), t0.Add(time.Minute)), // not due yet
		outItem("m0", "peer", t0, t0),
	} {
		if err := ob.Enqueue(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if got := dueIDs(t, ob, t0, 10); !equalStrings(got, []string{"m0", "m1", "m3"}) {
		t.Fatalf("Due(t0) = %v", got)
	}
	if got := dueIDs(t, ob, t0, 2); !equalStrings(got, []string{"m0", "m1"}) {
		t.Fatalf("Due limit 2 = %v", got)
	}
	if got := dueIDs(t, ob, t0.Add(time.Minute), 10); !equalStrings(got, []string{"m0", "m1", "m2", "m3"}) {
		t.Fatalf("Due(t0+1m) = %v", got)
	}

	// Sent to relay: queued items are no longer due.
	if err := ob.SetStatus(ctx, "m0", store.OutboxQueued, 1, t0); err != nil {
		t.Fatal(err)
	}
	// Failed attempt: pending with a backoff.
	if err := ob.SetStatus(ctx, "m1", store.OutboxPending, 1, t0.Add(core.BackoffMin)); err != nil {
		t.Fatal(err)
	}
	if got := dueIDs(t, ob, t0, 10); !equalStrings(got, []string{"m3"}) {
		t.Fatalf("Due after SetStatus = %v", got)
	}
	it, err := ob.Get(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if it.Attempts != 1 || !it.NextAttempt.Equal(t0.Add(core.BackoffMin)) || string(it.Envelope) != `{"id":"m1"}` {
		t.Fatalf("Get m1 = %+v", it)
	}
	if err := ob.SetStatus(ctx, "missing", store.OutboxPending, 0, t0); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("SetStatus missing err = %v", err)
	}
	if _, err := ob.Get(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get missing err = %v", err)
	}
}

func TestOutboxHoldRelease(t *testing.T) {
	ctx := context.Background()
	ob := newTestDB(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(ob.Enqueue(ctx, outItem("a1", "A", t0, t0)))
	must(ob.Enqueue(ctx, outItem("a2", "A", t0.Add(time.Second), t0.Add(time.Hour))))
	must(ob.Enqueue(ctx, outItem("b1", "B", t0, t0)))
	must(ob.SetStatus(ctx, "a2", store.OutboxQueued, 1, t0))

	must(ob.HoldPeer(ctx, "A"))
	for _, id := range []string{"a1", "a2"} {
		it, err := ob.Get(ctx, id)
		must(err)
		if it.Status != store.OutboxHeld {
			t.Fatalf("%s status = %s, want held", id, it.Status)
		}
	}
	if got := dueIDs(t, ob, t0.Add(2*time.Hour), 10); !equalStrings(got, []string{"b1"}) {
		t.Fatalf("Due while A held = %v", got)
	}

	release := t0.Add(30 * time.Minute)
	must(ob.ReleasePeer(ctx, "A", release))
	for _, id := range []string{"a1", "a2"} {
		it, err := ob.Get(ctx, id)
		must(err)
		if it.Status != store.OutboxPending || !it.NextAttempt.Equal(release) {
			t.Fatalf("%s after release = %+v", id, it)
		}
	}
	if got := dueIDs(t, ob, release, 10); !equalStrings(got, []string{"a1", "b1", "a2"}) {
		t.Fatalf("Due after release = %v", got)
	}
}

func TestOutboxDeletes(t *testing.T) {
	ctx := context.Background()
	ob := newTestDB(t)
	for _, it := range []store.OutboxItem{
		outItem("old", "A", t0, t0),
		outItem("x1", "A", t0.Add(core.OutboxRetention), t0),
		outItem("x2", "A", t0.Add(core.OutboxRetention), t0),
		outItem("y1", "B", t0.Add(core.OutboxRetention), t0),
	} {
		if err := ob.Enqueue(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := ob.Enqueue(ctx, outItem("x1", "A", t0, t0)); err == nil {
		t.Fatal("duplicate Enqueue succeeded")
	}
	n, err := ob.PurgeOutboxBefore(ctx, t0.Add(time.Millisecond))
	if err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v; want 1", n, err)
	}
	if err := ob.Delete(ctx, "x1", "x2", "nonexistent"); err != nil {
		t.Fatal(err)
	}
	if err := ob.Delete(ctx); err != nil {
		t.Fatalf("Delete with no ids: %v", err)
	}
	if err := ob.DeleteOutboxForPeer(ctx, "B"); err != nil {
		t.Fatal(err)
	}
	if got := dueIDs(t, ob, t0.Add(100*24*time.Hour), 10); len(got) != 0 {
		t.Fatalf("outbox not empty: %v", got)
	}
}

func TestOutboxCount(t *testing.T) {
	ctx := context.Background()
	ob := newTestDB(t)
	p, h, err := ob.CountOutbox(ctx)
	if err != nil || p != 0 || h != 0 {
		t.Fatalf("empty CountOutbox = %d, %d, %v", p, h, err)
	}
	for _, it := range []store.OutboxItem{
		outItem("p1", "A", t0, t0), outItem("p2", "A", t0, t0), outItem("q1", "A", t0, t0), outItem("h1", "B", t0, t0),
	} {
		if err := ob.Enqueue(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := ob.SetStatus(ctx, "q1", store.OutboxQueued, 1, t0); err != nil {
		t.Fatal(err)
	}
	if err := ob.HoldPeer(ctx, "B"); err != nil {
		t.Fatal(err)
	}
	p, h, err = ob.CountOutbox(ctx)
	if err != nil || p != 3 || h != 1 {
		t.Fatalf("CountOutbox = pending %d, held %d, %v; want 3, 1", p, h, err)
	}
}

func TestOutboxRequeueStale(t *testing.T) {
	ctx := context.Background()
	ob := newTestDB(t)
	for _, it := range []store.OutboxItem{
		outItem("stale", "peer", t0, t0),
		outItem("fresh", "peer", t0, t0),
		outItem("held", "peer", t0, t0),
		outItem("pend", "peer", t0, t0.Add(time.Hour)),
	} {
		if err := ob.Enqueue(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := ob.SetStatus(ctx, "stale", store.OutboxQueued, 1, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := ob.SetStatus(ctx, "fresh", store.OutboxQueued, 1, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := ob.SetStatus(ctx, "held", store.OutboxHeld, 1, t0); err != nil {
		t.Fatal(err)
	}
	n, err := ob.RequeueStale(ctx, t0.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("RequeueStale = %d, %v; want 1", n, err)
	}
	want := map[string]store.OutboxStatus{"stale": store.OutboxPending, "fresh": store.OutboxQueued,
		"held": store.OutboxHeld, "pend": store.OutboxPending}
	for id, st := range want {
		it, err := ob.Get(ctx, id)
		if err != nil || it.Status != st {
			t.Fatalf("%s: status %s, %v; want %s", id, it.Status, err, st)
		}
	}
	if got := dueIDs(t, ob, t0.Add(time.Minute), 10); !equalStrings(got, []string{"stale"}) {
		t.Fatalf("due after requeue = %v", got)
	}
}

// control.* items are never held: a held control.resumed would deadlock two peers
// that paused each other.
func TestOutboxHoldPeerSkipsControlItems(t *testing.T) {
	ctx := context.Background()
	ob := newTestDB(t)
	ctl := outItem("ctl", "A", t0, t0)
	ctl.Envelope = []byte(`{"id":"ctl","kind":"control.resumed"}`)
	chat := outItem("chat", "A", t0, t0)
	chat.Envelope = []byte(`{"id":"chat","kind":"chat"}`)
	for _, it := range []store.OutboxItem{ctl, chat} {
		if err := ob.Enqueue(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := ob.HoldPeer(ctx, "A"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]store.OutboxStatus{"ctl": store.OutboxPending, "chat": store.OutboxHeld} {
		if it, _ := ob.Get(ctx, id); it.Status != want {
			t.Fatalf("%s status = %s, want %s", id, it.Status, want)
		}
	}
}

// Items queued in the same millisecond leave in the order they were queued,
// whatever their IDs: a sender that queues a task update and then a
// link.closed must not have the close overtake the update.
func TestOutboxDueKeepsQueueOrderWithinATimestamp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.UnixMilli(1_700_000_000_000)
	// IDs chosen so that sorting by ID would reverse the queue order.
	queued := []string{"ZZZ", "MMM", "AAA"}
	for _, id := range queued {
		if err := db.Enqueue(ctx, outItem(id, "peer", at, at)); err != nil {
			t.Fatal(err)
		}
	}
	if got := dueIDs(t, db, at, 10); !equalStrings(got, queued) {
		t.Fatalf("due order %v, want queue order %v", got, queued)
	}
}

// An in-flight (sending) item is updated or deleted only while it is still
// sending; a concurrent change wins. It counts as pending, can be held, and
// returns to pending after its timeout (a daemon that stopped mid-send).
func TestOutboxSendingCompareAndSet(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	at := time.UnixMilli(1_700_000_000_000)
	if err := db.Enqueue(ctx, outItem("A", "peer", at, at)); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.SetStatusIf(ctx, "A", store.OutboxPending, store.OutboxSending, 0, at.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, _ := db.SetStatusIf(ctx, "A", store.OutboxPending, store.OutboxSending, 0, at); ok {
		t.Fatal("claimed twice")
	}
	if pending, held, _ := db.CountOutbox(ctx); pending != 1 || held != 0 {
		t.Fatalf("count while sending: pending %d held %d", pending, held)
	}
	// A reseal lands mid-send: the send's own bookkeeping must not apply.
	if err := db.SetStatus(ctx, "A", store.OutboxPending, 0, at); err != nil {
		t.Fatal(err)
	}
	if ok, _ := db.SetStatusIf(ctx, "A", store.OutboxSending, store.OutboxQueued, 1, at.Add(time.Hour)); ok {
		t.Fatal("queued overwrote the reseal")
	}
	if ok, _ := db.DeleteIf(ctx, "A", store.OutboxSending); ok {
		t.Fatal("deleted after the reseal")
	}
	if got := dueIDs(t, db, at, 10); !equalStrings(got, []string{"A"}) {
		t.Fatalf("due %v", got)
	}
	// Mid-send crash: sending past its timeout goes back to pending.
	if ok, _ := db.SetStatusIf(ctx, "A", store.OutboxPending, store.OutboxSending, 0, at.Add(time.Minute)); !ok {
		t.Fatal("reclaim")
	}
	if n, err := db.RequeueStale(ctx, at.Add(2*time.Minute)); err != nil || n != 1 {
		t.Fatalf("requeue stale sending: %d %v", n, err)
	}
	// A hold also covers an item in flight.
	if ok, _ := db.SetStatusIf(ctx, "A", store.OutboxPending, store.OutboxSending, 0, at.Add(time.Minute)); !ok {
		t.Fatal("reclaim")
	}
	if err := db.HoldPeer(ctx, "peer"); err != nil {
		t.Fatal(err)
	}
	if _, held, _ := db.CountOutbox(ctx); held != 1 {
		t.Fatalf("held %d, want 1", held)
	}
	if ok, _ := db.DeleteIf(ctx, "gone", store.OutboxSending); ok {
		t.Fatal("deleted a missing item")
	}
}
