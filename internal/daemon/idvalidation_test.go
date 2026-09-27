package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/sealing"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// hostileIDs are peer-chosen IDs that could spoof a terminal display.
var hostileIDs = []string{
	"01J8ZR0A1B2C3D4E5F6G7H8J\n\x1b[8m",
	"T1\nTask 2 of 2: 01J8ZR0A1B2C3D4E5F6G7H8J9K from boss",
	"\x1b[8m01J8ZR0A1B2C3D4E5F6G7H8J9K",
	"01j8zr0a1b2c3d4e5f6g7h8j9k",
	"T1",
}

func requireRejected(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted", what)
	}
	var re *RetryableError
	if errors.As(err, &re) {
		t.Fatalf("%s: rejection is retryable: %v", what, err)
	}
}

func TestHostileTaskIDsRejected(t *testing.T) {
	ctx := context.Background()
	for _, bad := range hostileIDs {
		e := d2Tasks(t, core.PermTasksAsk)
		peer, lctx := e.peer, withLink(ctx, e.link)

		env := d2Env(t, peer, core.KindTaskCreate, e.link.ID, core.TaskCreateBody{TaskID: bad, Instructions: "x"})
		requireRejected(t, "task.create "+bad, e.tasks.HandleCreate(lctx, peer, env))
		requireRejected(t, "rejected task.create "+bad, e.tasks.RejectCreate(lctx, peer, env))
		if _, err := e.st.GetTask(ctx, bad); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("hostile task %q stored: %v", bad, err)
		}
		if n, _ := e.tasks.PendingApprovals(ctx); n != 0 {
			t.Fatalf("hostile task %q awaits approval", bad)
		}
		if len(e.sender.ofKind(core.KindTaskUpdate)) != 0 {
			t.Fatalf("hostile task %q answered", bad)
		}

		upd := d2Env(t, peer, core.KindTaskUpdate, e.link.ID, core.TaskUpdateBody{TaskID: bad, State: core.TaskDone})
		requireRejected(t, "task.update "+bad, e.tasks.HandleUpdate(lctx, peer, upd))
		cancel := d2Env(t, peer, core.KindTaskCancel, e.link.ID, core.TaskCancelBody{TaskID: bad})
		requireRejected(t, "task.cancel "+bad, e.tasks.HandleCancel(lctx, peer, cancel))
		if items, _ := e.inbox.Check(ctx, e.session.ID, 10); len(items) != 0 {
			t.Fatalf("hostile task %q reached the inbox: %+v", bad, items)
		}
	}
}

func TestHostileTaskFileIDsRejected(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	id := core.NewID()
	env := d2Env(t, e.peer, core.KindTaskCreate, e.link.ID, core.TaskCreateBody{
		TaskID: id, Instructions: "x", Files: []core.FileRef{{FileID: "F\x1b[8m", Name: "a.txt", Size: 1}}})
	requireRejected(t, "task.create with a hostile file id", e.tasks.HandleCreate(withLink(ctx, e.link), e.peer, env))
	if _, err := e.st.GetTask(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("task with hostile file id stored: %v", err)
	}
}

func TestHostileFileOfferIDsRejected(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	peer := e.te.peer
	mutate := map[string]func(b *core.FileOfferBody){
		"file id":   func(b *core.FileOfferBody) { b.FileID = "01J8ZR0A1B2C3D4E5F6G7H8J\n\x1b[8m" },
		"short id":  func(b *core.FileOfferBody) { b.FileID = "F1" },
		"blob id":   func(b *core.FileOfferBody) { b.BlobID = "abc\n\x1b[8m" },
		"blob path": func(b *core.FileOfferBody) { b.BlobID = "../../v1/mailbox" },
		"task id":   func(b *core.FileOfferBody) { b.TaskID = "T1\x1b[8m" },
	}
	for name, m := range mutate {
		body := e.blobs.put(t, "a.txt", []byte("hello"))
		m(&body)
		env := d2Env(t, peer, core.KindFileOffer, e.te.link.ID, body)
		requireRejected(t, "file.offer "+name, e.files.HandleOffer(withLink(ctx, e.te.link), peer, env))
		e.files.Wait()
		if _, err := e.te.st.GetFile(ctx, body.FileID); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("%s: offer stored: %v", name, err)
		}
	}
}

func TestInboundDropsHostileMessageID(t *testing.T) {
	f := newInboundFixture(t)
	env, err := core.NewEnvelope(f.clock, f.gpu.id.MachineID(), f.me.MachineID(), core.KindChat, core.ChatBody{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// Control bytes are already refused by the frame header check; an ID
	// that is not a core ID (here: lower case, short) must be dropped too.
	env.ID = "t1"
	raw := sealFor(t, f.gpu.id, f.myPK, env)
	mb := f.run(t, transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: "d1", Frame: raw})
	if got := f.handledIDs(); len(got) != 0 {
		t.Fatalf("hostile message handled: %q", got)
	}
	if f.in.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", f.in.Dropped())
	}
	if len(deliveredIDs(t, f.sender)) != 0 {
		t.Fatal("receipt sent for a hostile message id")
	}
	if len(mb.acks) != 1 || mb.acks[0] != 1 {
		t.Fatal("hostile message not acked (it would be redelivered forever)")
	}
}

func sealFor(t *testing.T, from *keys.Identity, to keys.SignedPrekey, env core.Envelope) []byte {
	t.Helper()
	fr, err := sealing.Seal(from, to, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
