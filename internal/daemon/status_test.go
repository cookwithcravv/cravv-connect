package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestStatusReportsCounts(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk) // gpu-box, linked to session "lead"
	quiet, _ := d2Peer(t, e.st, "old-mac")
	away := d2Share(t, e.shared, "napping")
	if err := e.shared.AwayAll(ctx); err != nil { // both away
		t.Fatal(err)
	}
	gone := d2Share(t, e.shared, "gone")
	if err := e.shared.Close(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}

	act := NewPeerActivity(e.clock)
	chat := act.Wrap(d2Gated(e.st, e.shared, e.replies, NewChatHandler(e.inbox), nil))
	for range 2 {
		if err := chat.Handle(ctx, e.peer, d2Env(t, e.peer, core.KindChat, e.link.ID, core.ChatBody{Text: "hi"})); err != nil {
			t.Fatal(err)
		}
	}
	e.incoming(t, "needs approval")
	now := e.clock.Now()
	for i, st := range []store.OutboxStatus{store.OutboxPending, store.OutboxQueued, store.OutboxHeld} {
		it := store.OutboxItem{ID: core.NewID(), To: quiet.MachineID, Envelope: []byte("{}"), Status: st, NextAttempt: now, CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if err := e.st.Enqueue(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	slot := &mailboxSlot{}
	slot.set(newFakeMailbox(&callLog{}))
	versions := NewVersionNotices()
	versions.Replier(e.replies).Unsupported(ctx, quiet)
	svc := NewStatusService(StatusDeps{
		MachineID: "me", DeviceName: "mac", RelayURL: "https://relay.test", Mailboxes: slot,
		Killed: func() bool { return false }, Peers: e.st, Outbox: e.st, Shared: e.shared, Inbox: e.inbox,
		Tasks: e.tasks, Activity: act,
		Errors: []func() []string{func() []string { return []string{"relay offline: x"} }, versions.Errors},
	})
	got, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RelayConnected || got.Killed || got.MachineID != "me" || len(got.Peers) != 2 {
		t.Fatalf("status = %+v", got)
	}
	if !got.Online[e.peer.MachineID] || got.Online[quiet.MachineID] {
		t.Fatalf("online = %v", got.Online)
	}
	if s := strings.Join(got.Sessions, ","); len(got.Sessions) != 2 || !strings.Contains(s, "lead (away)") || !strings.Contains(s, away.Name+" (away)") {
		t.Fatalf("sessions = %v", got.Sessions)
	}
	if got.InboxUnread != 2 || got.PendingApprovals != 1 { // held tasks are not in the inbox
		t.Fatalf("unread %d approvals %d", got.InboxUnread, got.PendingApprovals)
	}
	if got.OutboxPending != 2 || got.OutboxHeld != 1 {
		t.Fatalf("outbox pending %d held %d", got.OutboxPending, got.OutboxHeld)
	}
	if len(got.Errors) != 2 || got.Errors[1] != "old-mac runs an older cravv-connect without session links: it needs an upgrade" {
		t.Fatalf("errors = %v", got.Errors)
	}

	e.clock.Advance(OnlineWindow + time.Second)
	slot.set(nil)
	got, _ = svc.Status(ctx)
	if got.RelayConnected || got.Online[e.peer.MachineID] {
		t.Fatalf("offline status = %+v", got)
	}
}
