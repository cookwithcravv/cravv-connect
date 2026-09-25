package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// A human decision re-checks the peer: approving a task or accepting a file
// from a peer that is gone, paused, or no longer trusted enough is refused.
func TestDecideAndAcceptRecheckThePeer(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	st := e.te.st

	ask, _ := d2Peer(t, st, "mac", core.TrustAskFirst)
	paused := e.te.incoming(t, ask, "", "a")
	ask.Paused = true
	mustPut(t, st, ask)
	if err := e.te.tasks.Decide(ctx, paused, true, true); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("approve from a paused peer err = %v", err)
	}
	ask.Paused = false
	ask.TrustIn = core.TrustChatOnly // lowered without the re-check hook
	mustPut(t, st, ask)
	if err := e.te.tasks.Decide(ctx, paused, true, true); !errors.Is(err, core.ErrNotPermitted) {
		t.Fatalf("approve from a chat-only peer err = %v", err)
	}
	if err := st.DeletePeer(ctx, ask.MachineID); err != nil {
		t.Fatal(err)
	}
	if err := e.te.tasks.Decide(ctx, paused, true, true); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("approve from an unpaired peer err = %v", err)
	}
	if tk := e.te.state(t, paused); tk.State != core.TaskAwaitingApproval {
		t.Fatalf("task state after refused approvals = %s", tk.State)
	}
	if err := e.te.tasks.Decide(ctx, paused, false, true); err != nil {
		t.Fatalf("deny still works: %v", err)
	}

	stranger, _ := d2Peer(t, st, "stranger", core.TrustChatOnly)
	body := e.blobs.put(t, "a.txt", []byte("a"))
	e.offer(t, stranger, body)
	stranger.Paused = true
	mustPut(t, st, stranger)
	if err := e.files.Accept(ctx, body.FileID, true); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("accept from a paused peer err = %v", err)
	}
	if err := st.DeletePeer(ctx, stranger.MachineID); err != nil {
		t.Fatal(err)
	}
	if err := e.files.Accept(ctx, body.FileID, true); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("accept from an unpaired peer err = %v", err)
	}
	if r := e.record(t, body.FileID); r.State != store.FileHeld {
		t.Fatalf("file state after refused accepts = %s", r.State)
	}
}

// Pausing or unpairing a peer rejects its tasks awaiting approval and declines
// its held files, so nothing waits for a human decision that can no longer apply.
func TestPauseAndUnpairClearPendingDecisions(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	st := e.te.st
	peers := NewPeerService(st, &mailboxSlot{}, &recordingSender{}, &d2Audit{}, e.te.clock)
	peers.AddCutOffObserver(e.te.tasks)
	peers.AddCutOffObserver(e.files)

	ask, _ := d2Peer(t, st, "mac", core.TrustAskFirst)
	held := e.te.incoming(t, ask, "", "a")
	if err := peers.Pause(ctx, "mac"); err != nil {
		t.Fatal(err)
	}
	if tk := e.te.state(t, held); tk.State != core.TaskRejected {
		t.Fatalf("held task after pause = %s", tk.State)
	}
	ups := d2Updates(t, e.te.sender)
	if last := ups[len(ups)-1]; last.TaskID != held || last.State != core.TaskRejected {
		t.Fatalf("sender not told: %+v", last)
	}

	stranger, _ := d2Peer(t, st, "stranger", core.TrustChatOnly)
	body := e.blobs.put(t, "a.txt", []byte("a"))
	e.offer(t, stranger, body)
	other := e.blobs.put(t, "b.txt", []byte("b"))
	e.offer(t, stranger, other)
	if err := peers.Pause(ctx, "stranger"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{body.FileID, other.FileID} {
		if r := e.record(t, id); r.State != store.FileDeclined || r.Reason != "peer paused" {
			t.Fatalf("held file after pause = %s (%s)", r.State, r.Reason)
		}
	}

	ask2, _ := d2Peer(t, st, "mac2", core.TrustAskFirst)
	held2 := e.te.incoming(t, ask2, "", "b")
	chatOnly, _ := d2Peer(t, st, "stranger2", core.TrustChatOnly)
	third := e.blobs.put(t, "c.txt", []byte("c"))
	e.offer(t, chatOnly, third)
	if err := peers.Unpair(ctx, "mac2"); err != nil {
		t.Fatal(err)
	}
	if err := peers.RemoveByPeer(ctx, chatOnly.MachineID); err != nil {
		t.Fatal(err)
	}
	if tk := e.te.state(t, held2); tk.State != core.TaskRejected {
		t.Fatalf("held task after unpair = %s", tk.State)
	}
	if r := e.record(t, third.FileID); r.State != store.FileDeclined {
		t.Fatalf("held file after the peer unpaired = %s", r.State)
	}
}

func TestDaemonWiresCutOffObservers(t *testing.T) {
	ctx := context.Background()
	d := d2NewDaemon(t, t.TempDir(), &d2Relay{})
	defer d.Close()
	peer := newTestPeer(t, "mac", core.TrustAskFirst)
	mustPut(t, d.store, peer.rec)
	id := core.NewID()
	env := d2Env(t, peer.rec, core.KindTaskCreate, "", "", core.TaskCreateBody{TaskID: id, Instructions: "x"})
	if err := d.Tasks().HandleCreate(ctx, peer.rec, env); err != nil {
		t.Fatal(err)
	}
	if err := d.Peers().Pause(ctx, "mac"); err != nil {
		t.Fatal(err)
	}
	if tk, _ := d.store.GetTask(ctx, id); tk.State != core.TaskRejected {
		t.Fatalf("held task after pause = %s", tk.State)
	}
}
