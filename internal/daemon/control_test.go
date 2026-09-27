package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

type controlFixture struct {
	reg   *HandlerRegistry
	peers *memPeers
	out   *Outbound
	obox  *memOutbox
	svc   *PeerService
	mb    *fakeMailbox
	audit *recordingAudit
	gpu   testPeer
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	me, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	clock := core.NewFakeClock(testEpoch)
	f := &controlFixture{reg: NewHandlerRegistry(), peers: newMemPeers(), obox: newMemOutbox(), audit: &recordingAudit{}}
	f.mb = newFakeMailbox(nil)
	slot := &mailboxSlot{}
	slot.set(f.mb)
	f.out = NewOutbound(me, f.peers, f.obox, slot, clock, nil, nil)
	f.svc = NewPeerService(f.peers, slot, f.out, f.audit, clock)
	RegisterControlHandlers(f.reg, f.peers, f.svc, f.out)
	f.gpu = newTestPeer(t, "gpu-box")
	mustPut(t, f.peers, f.gpu.rec)
	return f
}

func (f *controlFixture) handle(t *testing.T, kind core.Kind, body any) error {
	t.Helper()
	h, ok := f.reg.Lookup(kind)
	if !ok {
		t.Fatalf("no handler registered for %s", kind)
	}
	env, err := core.NewEnvelope(core.NewFakeClock(testEpoch), f.gpu.rec.MachineID, "me", kind, body)
	if err != nil {
		t.Fatal(err)
	}
	return h.Handle(context.Background(), mustGetPeer(t, f.peers, f.gpu.rec.MachineID), env)
}

func (f *controlFixture) sendTo(t *testing.T, to core.MachineID) string {
	t.Helper()
	id, err := f.out.SendEnvelope(context.Background(), to, core.KindChat, "01JLINK", core.ChatBody{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *controlFixture) newerPrekey(t *testing.T, signer *keys.Identity, age time.Duration) core.SignedPrekeyWire {
	t.Helper()
	pk, err := keys.GeneratePrekey(testEpoch.Add(age))
	if err != nil {
		t.Fatal(err)
	}
	return pk.Signed(signer).Wire()
}

func TestAllControlKindsRegistered(t *testing.T) {
	f := newControlFixture(t)
	for _, k := range []core.Kind{core.KindControlPrekey, core.KindControlStalePrekey, core.KindControlDelivered,
		core.KindControlPaused, core.KindControlResumed, core.KindControlUnpaired, core.KindControlRelayMoved} {
		if _, ok := f.reg.Lookup(k); !ok {
			t.Errorf("%s not registered", k)
		}
	}
}

func TestPrekeyHandler(t *testing.T) {
	f := newControlFixture(t)
	newer := f.newerPrekey(t, f.gpu.id, time.Hour)
	if err := f.handle(t, core.KindControlPrekey, core.PrekeyBody{Prekey: newer}); err != nil {
		t.Fatal(err)
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).Prekey.ID; got != newer.ID {
		t.Fatalf("prekey = %s, want %s", got, newer.ID)
	}

	older := f.newerPrekey(t, f.gpu.id, -time.Hour)
	if err := f.handle(t, core.KindControlPrekey, core.PrekeyBody{Prekey: older}); err != nil {
		t.Fatal(err)
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).Prekey.ID; got != newer.ID {
		t.Fatal("an older prekey replaced a newer one")
	}

	stranger, _ := keys.GenerateIdentity()
	forged := f.newerPrekey(t, stranger, 2*time.Hour)
	if err := f.handle(t, core.KindControlPrekey, core.PrekeyBody{Prekey: forged}); err == nil {
		t.Fatal("accepted a prekey not signed by the peer")
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).Prekey.ID; got != newer.ID {
		t.Fatal("forged prekey stored")
	}
}

func TestStalePrekeyHandler(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()
	other := newTestPeer(t, "other")
	mustPut(t, f.peers, other.rec)
	mine := f.sendTo(t, f.gpu.rec.MachineID)
	theirs := f.sendTo(t, other.rec.MachineID)
	for _, id := range []string{mine, theirs} {
		if err := f.obox.SetStatus(ctx, id, store.OutboxQueued, 1, testEpoch); err != nil {
			t.Fatal(err)
		}
	}
	current := f.newerPrekey(t, f.gpu.id, time.Hour)
	if err := f.handle(t, core.KindControlStalePrekey, core.StalePrekeyBody{MsgID: mine, Prekey: current}); err != nil {
		t.Fatal(err)
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).Prekey.ID; got != current.ID {
		t.Fatal("prekey not updated")
	}
	if it, _ := f.obox.item(mine); it.Status != store.OutboxPending {
		t.Fatalf("own item status = %s, want pending", it.Status)
	}
	// gpu-box names an item addressed to another peer: ignored.
	if err := f.handle(t, core.KindControlStalePrekey, core.StalePrekeyBody{MsgID: theirs, Prekey: current}); err != nil {
		t.Fatal(err)
	}
	if it, _ := f.obox.item(theirs); it.Status != store.OutboxQueued {
		t.Fatalf("other peer's item status = %s, want queued", it.Status)
	}
	if err := f.handle(t, core.KindControlStalePrekey, core.StalePrekeyBody{MsgID: "gone", Prekey: current}); err != nil {
		t.Fatalf("unknown msg id must be ignored: %v", err)
	}
}

func TestDeliveredHandler(t *testing.T) {
	f := newControlFixture(t)
	a := f.sendTo(t, f.gpu.rec.MachineID)
	b := f.sendTo(t, f.gpu.rec.MachineID)
	if err := f.handle(t, core.KindControlDelivered, core.DeliveredBody{IDs: []string{a}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.obox.item(a); ok {
		t.Fatal("delivered item kept")
	}
	if _, ok := f.obox.item(b); !ok {
		t.Fatal("undelivered item removed")
	}
}

func TestPausedAndResumedHandlers(t *testing.T) {
	f := newControlFixture(t)
	id := f.sendTo(t, f.gpu.rec.MachineID)
	if err := f.handle(t, core.KindControlPaused, core.EmptyBody{}); err != nil {
		t.Fatal(err)
	}
	if !mustGetPeer(t, f.peers, f.gpu.rec.MachineID).PausedByPeer {
		t.Fatal("PausedByPeer not set")
	}
	if it, _ := f.obox.item(id); it.Status != store.OutboxHeld {
		t.Fatalf("status = %s, want held", it.Status)
	}
	if err := f.handle(t, core.KindControlResumed, core.EmptyBody{}); err != nil {
		t.Fatal(err)
	}
	if mustGetPeer(t, f.peers, f.gpu.rec.MachineID).PausedByPeer {
		t.Fatal("PausedByPeer not cleared")
	}
	if it, _ := f.obox.item(id); it.Status != store.OutboxPending {
		t.Fatalf("status = %s, want pending", it.Status)
	}
}

func TestUnpairedHandler(t *testing.T) {
	f := newControlFixture(t)
	f.sendTo(t, f.gpu.rec.MachineID)
	if err := f.handle(t, core.KindControlUnpaired, core.EmptyBody{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.peers.GetPeer(context.Background(), f.gpu.rec.MachineID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("peer kept after control.unpaired")
	}
	if f.obox.len() != 0 {
		t.Fatal("outbox kept after control.unpaired")
	}
	if !f.mb.isDenied(f.gpu.rec.IK) {
		t.Fatal("peer not denied")
	}
	if len(f.mb.sentFrames()) != 0 {
		t.Fatal("sent something back to the peer that unpaired us")
	}
	ev := f.audit.last()
	if ev.Type != audit.EvUnpair || ev.Detail["by_peer"] != true {
		t.Fatalf("audit = %+v", ev)
	}
}

func TestRelayMovedHandler(t *testing.T) {
	f := newControlFixture(t)
	if err := f.handle(t, core.KindControlRelayMoved, core.RelayMovedBody{RelayURL: "https://new.relay.test"}); err != nil {
		t.Fatal(err)
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).RelayURL; got != "https://new.relay.test" {
		t.Fatalf("relay url = %q", got)
	}
	for _, bad := range []string{"", "ftp://x", "javascript:alert(1)", "https://", "http://relay.example"} {
		if err := f.handle(t, core.KindControlRelayMoved, core.RelayMovedBody{RelayURL: bad}); err == nil {
			t.Errorf("accepted relay url %q", bad)
		}
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).RelayURL; got != "https://new.relay.test" {
		t.Fatalf("bad url overwrote the stored one: %q", got)
	}
}
