package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// A human decision re-checks the link: approving a task on a link that no
// longer allows tasks, or that closed, is refused; denying still works.
func TestDecideRechecksTheLink(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk)
	first := e.incoming(t, "a")
	second := e.incoming(t, "b")
	setLink := func(mut func(*store.Link)) {
		if _, err := e.st.UpdateLink(ctx, e.peer.MachineID, e.link.ID, func(l *store.Link) error {
			mut(l)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	setLink(func(l *store.Link) { l.PermissionIn = core.PermMessages }) // lowered without the observers
	if err := e.tasks.Decide(ctx, first, true, AuthPassword); !errors.Is(err, core.ErrNotPermitted) {
		t.Fatalf("approve on a messages link err = %v", err)
	}
	setLink(func(l *store.Link) { l.PermissionIn, l.State = core.PermTasksAsk, store.LinkClosed })
	if err := e.tasks.Decide(ctx, second, true, AuthPassword); !errors.Is(err, core.ErrLinkClosed) {
		t.Fatalf("approve on a closed link err = %v", err)
	}
	for _, id := range []string{first, second} {
		if tk := e.state(t, id); tk.State != core.TaskAwaitingApproval {
			t.Fatalf("task state after the refused approval = %s", tk.State)
		}
	}
	if err := e.tasks.Decide(ctx, second, false, AuthNone); err != nil {
		t.Fatalf("deny still works: %v", err)
	}
}

// Pausing or unpairing a peer closes its links, which rejects or fails the
// tasks on them and declines held files, so nothing waits for a decision
// that can no longer apply.
func TestPauseAndUnpairClearPendingDecisions(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	st := e.te.st
	peers := NewPeerService(st, &mailboxSlot{}, &recordingSender{}, &d2Audit{}, e.te.clock)
	peers.AddCutOffObserver(e.te.tasks)
	peers.AddCutOffObserver(e.files)
	peers.AddCutOffObserver(e.te.links)

	ask, _ := d2Peer(t, st, "mac")
	askLink := d2Link(t, st, ask, e.te.session, "asker", core.PermTasksAsk, core.PermMessages)
	held := core.NewID()
	if err := e.te.handle(t, d2Env(t, ask, core.KindTaskCreate, askLink.ID, core.TaskCreateBody{TaskID: held, Instructions: "a"})); err != nil {
		t.Fatal(err)
	}
	if err := peers.Pause(ctx, "mac"); err != nil {
		t.Fatal(err)
	}
	if tk := e.te.state(t, held); tk.State != core.TaskRejected {
		t.Fatalf("held task after pause = %s", tk.State)
	}
	if l, _ := st.GetLink(ctx, ask.MachineID, askLink.ID); l.State != store.LinkClosed || l.Reason != core.ClosePaused {
		t.Fatalf("link after pause = %+v", l)
	}
	ups := d2Updates(t, e.te.sender)
	if last := ups[len(ups)-1]; last.TaskID != held || last.State != core.TaskRejected {
		t.Fatalf("sender not told: %+v", last)
	}

	ask2, _ := d2Peer(t, st, "mac2")
	link2 := d2Link(t, st, ask2, e.te.session, "asker", core.PermTasksAuto, core.PermMessages)
	queued := core.NewID()
	if err := e.te.handle(t, d2Env(t, ask2, core.KindTaskCreate, link2.ID, core.TaskCreateBody{TaskID: queued, Instructions: "b"})); err != nil {
		t.Fatal(err)
	}
	if err := peers.Unpair(ctx, "mac2"); err != nil {
		t.Fatal(err)
	}
	if tk := e.te.state(t, queued); tk.State != core.TaskFailed {
		t.Fatalf("queued task after unpair = %s", tk.State)
	}
	if l, _ := st.GetLink(ctx, ask2.MachineID, link2.ID); l.State != store.LinkClosed || l.Reason != core.CloseUnpaired {
		t.Fatalf("link after unpair = %+v", l)
	}
}

func TestDaemonWiresCutOffObservers(t *testing.T) {
	ctx := context.Background()
	d := d2NewDaemon(t, t.TempDir(), &d2Relay{})
	defer d.Close()
	peer := newTestPeer(t, "mac")
	mustPut(t, d.store, peer.rec)
	session := d2Share(t, d.Shared(), "lead")
	link := d2Link(t, d.store, peer.rec, session, "trainer", core.PermTasksAsk, core.PermMessages)
	id := core.NewID()
	env := d2Env(t, peer.rec, core.KindTaskCreate, link.ID, core.TaskCreateBody{TaskID: id, Instructions: "x"})
	h, _ := d.svc.Load().registry.Lookup(core.KindTaskCreate)
	if err := h.Handle(ctx, peer.rec, env); err != nil {
		t.Fatal(err)
	}
	if tk, _ := d.store.GetTask(ctx, id); tk.State != core.TaskAwaitingApproval {
		t.Fatalf("task through the daemon's gate = %s", tk.State)
	}
	if err := d.Peers().Pause(ctx, "mac"); err != nil {
		t.Fatal(err)
	}
	if tk, _ := d.store.GetTask(ctx, id); tk.State != core.TaskRejected && tk.State != core.TaskFailed {
		t.Fatalf("held task after pause = %s", tk.State)
	}
	if l, _ := d.store.GetLink(ctx, peer.rec.MachineID, link.ID); l.State != store.LinkClosed {
		t.Fatalf("link after pause = %+v", l)
	}
}
