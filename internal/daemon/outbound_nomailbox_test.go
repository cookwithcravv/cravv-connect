package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

func unknownMailbox(n int) []transport.SendStatus {
	out := make([]transport.SendStatus, n)
	for i := range out {
		out[i] = transport.SendUnknownMailbox
	}
	return out
}

// A peer that just paired or set up again may have no mailbox on the relay
// for a while: its items wait (with a backoff that grows to
// NoMailboxRetryMax), status says so, and they go out once it has one.
func TestOutboundUnknownMailboxWaits(t *testing.T) {
	f := newOutboundFixture(t)
	f.mb.statuses = unknownMailbox(100)
	id := f.send(t, "hello")
	f.pass(t)
	it := f.status(t, id)
	if it.Status != store.OutboxPending || !it.NextAttempt.Equal(f.clock.Now().Add(time.Second)) {
		t.Fatalf("after unknown_mailbox: %s next +%v, want pending +1s", it.Status, it.NextAttempt.Sub(f.clock.Now()))
	}
	want := []string{"gpu-box has no mailbox on this relay yet; 1 messages waiting"}
	if got := f.o.Errors(); !slices.Equal(got, want) {
		t.Fatalf("errors = %q, want %q", got, want)
	}
	// The backoff doubles up to NoMailboxRetryMax and stays there.
	var last time.Duration
	for range 15 {
		f.clock.Advance(f.status(t, id).NextAttempt.Sub(f.clock.Now()))
		f.pass(t)
		last = f.status(t, id).NextAttempt.Sub(f.clock.Now())
	}
	if last != NoMailboxRetryMax {
		t.Fatalf("backoff = %v, want %v", last, NoMailboxRetryMax)
	}
	if got := f.o.Errors(); !slices.Equal(got, want) {
		t.Fatalf("errors while waiting = %q", got)
	}

	// The peer registers: a new message goes out, and the waiting one is
	// retried at once rather than after its long backoff.
	f.mb.statuses = nil
	id2 := f.send(t, "again")
	f.pass(t)
	f.pass(t)
	for _, x := range []string{id, id2} {
		if st := f.status(t, x).Status; st != store.OutboxQueued {
			t.Fatalf("%s status = %s, want queued", x, st)
		}
	}
	if got := f.o.Errors(); len(got) != 0 {
		t.Fatalf("errors after the peer registered = %q", got)
	}
}

// An item still without a mailbox when it expires is dropped, and only then
// does status report the drop.
func TestOutboundUnknownMailboxDropsOnExpiry(t *testing.T) {
	f := newOutboundFixture(t)
	f.mb.statuses = unknownMailbox(100)
	id := f.send(t, "hello")
	f.pass(t)
	f.clock.Advance(core.OutboxRetention)
	f.pass(t)
	if _, ok := f.outbox.item(id); ok {
		t.Fatal("an expired item was kept")
	}
	got := f.o.Errors()
	if len(got) != 1 || !strings.Contains(got[0], "dropped message "+id+": gpu-box has no mailbox on this relay") {
		t.Fatalf("errors = %q", got)
	}
}

// The outbox purge can expire a waiting item before its next try: status
// then reports the drop instead of a message that no longer waits.
func TestOutboundUnknownMailboxPurged(t *testing.T) {
	ctx := context.Background()
	f := newOutboundFixture(t)
	f.mb.statuses = unknownMailbox(100)
	id := f.send(t, "hello")
	f.pass(t)
	f.clock.Advance(core.OutboxRetention + time.Second)
	if _, err := f.o.PurgeOld(ctx); err != nil {
		t.Fatal(err)
	}
	got := f.o.Errors()
	if len(got) != 1 || !strings.Contains(got[0], "dropped message "+id) {
		t.Fatalf("errors = %q", got)
	}
}

// Unpairing forgets the waiting items and their warning.
func TestOutboundUnknownMailboxForget(t *testing.T) {
	f := newOutboundFixture(t)
	f.mb.statuses = unknownMailbox(100)
	f.send(t, "hello")
	f.pass(t)
	if err := f.o.Forget(context.Background(), f.gpu.rec.MachineID); err != nil {
		t.Fatal(err)
	}
	if got := f.o.Errors(); len(got) != 0 {
		t.Fatalf("errors after forget = %q", got)
	}
}

// A relay's per-request internal error (res{status:"error",code:"internal"})
// fails that send only: the item backs off and goes out on a later pass over
// the same connection.
func TestOutboundRelayInternalErrorIsRetried(t *testing.T) {
	f := newOutboundFixture(t)
	f.mb.sendErr = fmt.Errorf("relay: internal: %w", transport.ErrRelayInternal)
	id := f.send(t, "hello")
	f.pass(t)
	it := f.status(t, id)
	if it.Status != store.OutboxPending || it.Attempts != 1 || !it.NextAttempt.Equal(f.clock.Now().Add(time.Second)) {
		t.Fatalf("after an internal error: %s attempts %d next +%v", it.Status, it.Attempts, it.NextAttempt.Sub(f.clock.Now()))
	}
	if mb, ok := f.slot.Mailbox(); !ok || mb != f.mb {
		t.Fatal("the connection was dropped after an internal error")
	}
	f.mb.sendErr = nil
	f.clock.Advance(time.Second)
	f.pass(t)
	if st := f.status(t, id).Status; st != store.OutboxQueued {
		t.Fatalf("status after the retry = %s, want queued", st)
	}
}

// A message still waiting for the peer's mailbox can be withdrawn; one that
// went out cannot.
func TestOutboundWithdrawUnsent(t *testing.T) {
	ctx := context.Background()
	f := newOutboundFixture(t)
	peer := f.gpu.rec.MachineID
	f.mb.statuses = unknownMailbox(1)
	f.send(t, "waiting")
	f.pass(t)
	sent := f.send(t, "sent")
	f.pass(t) // the relay has a mailbox now: both go out on this pass or the next
	if ok, err := f.o.WithdrawUnsent(ctx, peer, sent); ok || err != nil {
		t.Fatalf("withdrew a message that went out: %v %v", ok, err)
	}

	g := newOutboundFixture(t)
	g.mb.statuses = unknownMailbox(10)
	waiting := g.send(t, "waiting")
	g.pass(t)
	if ok, err := g.o.WithdrawUnsent(ctx, g.gpu.rec.MachineID, waiting); !ok || err != nil {
		t.Fatalf("WithdrawUnsent = %v %v, want true", ok, err)
	}
	if _, ok := g.outbox.item(waiting); ok {
		t.Fatal("the withdrawn message is still in the outbox")
	}
	if got := g.o.Errors(); len(got) != 0 {
		t.Fatalf("errors after withdrawing = %q", got)
	}
}
