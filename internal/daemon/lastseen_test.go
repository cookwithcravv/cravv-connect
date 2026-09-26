package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestPeerActivityLastSeen(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(d2Epoch)
	act := NewPeerActivity(clock)
	if _, ok := act.LastSeen("m1"); ok {
		t.Fatal("a peer never heard from has a last seen time")
	}
	h := act.Wrap(HandlerFunc(func(context.Context, store.Peer, core.Envelope) error { return nil }))
	if err := h.Handle(ctx, store.Peer{MachineID: "m1"}, core.Envelope{}); err != nil {
		t.Fatal(err)
	}
	heard := clock.Now()
	clock.Advance(OnlineWindow + time.Minute)
	got, ok := act.LastSeen("m1")
	if !ok || !got.Equal(heard) {
		t.Fatalf("LastSeen = %v %v, want %v", got, ok, heard)
	}
	if act.Online("m1") {
		t.Fatal("still online after the window")
	}
}

// Status reports when each peer was last heard from, also after it went
// offline, and nothing for a peer never heard from.
func TestStatusReportsLastSeen(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk)
	quiet, _ := d2Peer(t, e.st, "old-mac")
	act := NewPeerActivity(e.clock)
	h := act.Wrap(HandlerFunc(func(context.Context, store.Peer, core.Envelope) error { return nil }))
	if err := h.Handle(ctx, e.peer, core.Envelope{}); err != nil {
		t.Fatal(err)
	}
	heard := e.clock.Now()
	e.clock.Advance(OnlineWindow + time.Second)
	slot := &mailboxSlot{}
	slot.set(newFakeMailbox(&callLog{}))
	svc := NewStatusService(StatusDeps{
		MachineID: "me", Mailboxes: slot, Killed: func() bool { return false },
		Peers: e.st, Outbox: e.st, Shared: e.shared, Inbox: e.inbox, Tasks: e.tasks, Activity: act,
	})
	got, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSeen[e.peer.MachineID].Equal(heard) || got.Online[e.peer.MachineID] {
		t.Fatalf("peer: last seen %v online %v, want %v and offline", got.LastSeen[e.peer.MachineID], got.Online[e.peer.MachineID], heard)
	}
	if at, ok := got.LastSeen[quiet.MachineID]; ok {
		t.Fatalf("quiet peer last seen %v, want none", at)
	}
}
