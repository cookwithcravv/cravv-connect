package daemon

import (
	"context"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Control kinds are never confirmed with control.delivered, so once the relay
// queued one it must leave the outbox; otherwise status counts it as pending
// until the outbox purge, and the stale requeue would resend it every RelayTTL
// (found by the e2e outbox-drain check).
func TestOutboundDropsQueuedControlItems(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	ctl, err := f.o.SendEnvelope(ctx, f.gpu.rec.MachineID, core.KindControlDelivered, "", core.DeliveredBody{IDs: []string{"X"}})
	if err != nil {
		t.Fatal(err)
	}
	chat := f.send(t, "still waits for its receipt")
	f.pass(t)
	if n := len(f.mb.sentFrames()); n != 2 {
		t.Fatalf("sent %d frames, want 2", n)
	}
	if _, ok := f.outbox.item(ctl); ok {
		t.Fatal("queued control item still in the outbox")
	}
	if st := f.status(t, chat).Status; st != store.OutboxQueued {
		t.Fatalf("chat item status %s, want queued", st)
	}
	pending, held, err := f.outbox.CountOutbox(ctx)
	if err != nil || pending != 1 || held != 0 {
		t.Fatalf("CountOutbox = %d, %d, %v; want 1, 0", pending, held, err)
	}
}
