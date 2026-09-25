package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestStatusReportsCounts(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	active, _ := d2Peer(t, e.st, "gpu-box", core.TrustAskFirst)
	quiet, _ := d2Peer(t, e.st, "old-mac", core.TrustAutonomous)
	live, _ := e.reg.Register(ctx, "claude", "/w/a")
	gone, _ := e.reg.Register(ctx, "codex", "/w/b")
	e.reg.Disconnect(ctx, gone)

	act := NewPeerActivity(e.clock)
	chat := act.Wrap(NewChatHandler(e.inbox))
	for i := 0; i < 2; i++ {
		if err := chat.Handle(ctx, active, d2Env(t, active, core.KindChat, "", "", core.ChatBody{Text: "hi"})); err != nil {
			t.Fatal(err)
		}
	}
	e.incoming(t, active, "", "needs approval")
	now := e.clock.Now()
	for i, st := range []store.OutboxStatus{store.OutboxPending, store.OutboxQueued, store.OutboxHeld} {
		it := store.OutboxItem{ID: core.NewID(), To: quiet.MachineID, Envelope: []byte("{}"), Status: st, NextAttempt: now, CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if err := e.st.Enqueue(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	slot := &mailboxSlot{}
	slot.set(newFakeMailbox(&callLog{}))
	svc := NewStatusService(StatusDeps{
		MachineID: "me", DeviceName: "mac", RelayURL: "https://relay.test", Mailboxes: slot,
		Killed: func() bool { return false }, Peers: e.st, Outbox: e.st, Sessions: e.reg, Inbox: e.inbox,
		Tasks: e.tasks, Activity: act, Errors: []func() []string{func() []string { return []string{"relay offline: x"} }},
	})
	got, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RelayConnected || got.Killed || got.MachineID != "me" || len(got.Peers) != 2 {
		t.Fatalf("status = %+v", got)
	}
	if !got.Online[active.MachineID] || got.Online[quiet.MachineID] {
		t.Fatalf("online = %v", got.Online)
	}
	if len(got.Sessions) != 1 || got.Sessions[0] != live {
		t.Fatalf("sessions = %v", got.Sessions)
	}
	if got.InboxUnread != 2 || got.PendingApprovals != 1 { // held tasks are not in the inbox
		t.Fatalf("unread %d approvals %d", got.InboxUnread, got.PendingApprovals)
	}
	if got.OutboxPending != 2 || got.OutboxHeld != 1 {
		t.Fatalf("outbox pending %d held %d", got.OutboxPending, got.OutboxHeld)
	}
	if len(got.Errors) != 1 {
		t.Fatalf("errors = %v", got.Errors)
	}

	e.clock.Advance(OnlineWindow + time.Second)
	slot.set(nil)
	got, _ = svc.Status(ctx)
	if got.RelayConnected || got.Online[active.MachineID] {
		t.Fatalf("offline status = %+v", got)
	}
}
