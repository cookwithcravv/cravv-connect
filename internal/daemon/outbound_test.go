package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

type outboundFixture struct {
	o      *Outbound
	peers  *memPeers
	outbox *memOutbox
	mb     *fakeMailbox
	slot   *mailboxSlot
	clock  *core.FakeClock
	me     *keys.Identity
	gpu    testPeer
	killed bool
}

func newOutboundFixture(t *testing.T) *outboundFixture {
	t.Helper()
	me, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	f := &outboundFixture{peers: newMemPeers(), outbox: newMemOutbox(), slot: &mailboxSlot{},
		clock: core.NewFakeClock(testEpoch), me: me}
	f.mb = newFakeMailbox(nil)
	f.slot.set(f.mb)
	f.o = NewOutbound(me, f.peers, f.outbox, f.slot, f.clock, func() bool { return f.killed }, nil)
	f.gpu = newTestPeer(t, "gpu-box", core.TrustAskFirst)
	mustPut(t, f.peers, f.gpu.rec)
	return f
}

func (f *outboundFixture) send(t *testing.T, text string) string {
	t.Helper()
	id, err := f.o.SendEnvelope(context.Background(), f.gpu.rec.MachineID, core.KindChat, "claude@proj", "", core.ChatBody{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *outboundFixture) pass(t *testing.T) {
	t.Helper()
	if err := f.o.SendDue(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (f *outboundFixture) status(t *testing.T, id string) store.OutboxItem {
	t.Helper()
	it, ok := f.outbox.item(id)
	if !ok {
		t.Fatalf("outbox item %s missing", id)
	}
	return it
}

// openAs opens a sent frame the way the peer would, with the given prekeys.
func openAs(t *testing.T, me *keys.Identity, peer testPeer, raw []byte, pks ...*keys.Prekey) (sealing.Frame, core.Envelope, error) {
	t.Helper()
	fr, err := sealing.ParseFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	res := resolverMap{}
	for _, pk := range pks {
		res[pk.ID] = pk
	}
	env, err := sealing.Open(fr, me.Public(), peer.rec.MachineID, res)
	return fr, env, err
}

func TestOutboundSendsSealedFrameAndMarksQueued(t *testing.T) {
	f := newOutboundFixture(t)
	id := f.send(t, "hello")
	f.pass(t)
	sent := f.mb.sentFrames()
	if len(sent) != 1 || sent[0].ID != id || sent[0].To != f.gpu.rec.MachineID {
		t.Fatalf("sent = %+v", sent)
	}
	fr, env, err := openAs(t, f.me, f.gpu, sent[0].Frame, f.gpu.prekey)
	if err != nil {
		t.Fatalf("peer cannot open frame: %v", err)
	}
	if fr.Header.PKID != f.gpu.prekey.ID {
		t.Fatalf("sealed to %s, want %s", fr.Header.PKID, f.gpu.prekey.ID)
	}
	var body core.ChatBody
	if err := json.Unmarshal(env.Body, &body); err != nil || body.Text != "hello" || env.FromSession != "claude@proj" {
		t.Fatalf("envelope = %+v (%v)", env, err)
	}
	if st := f.status(t, id).Status; st != store.OutboxQueued {
		t.Fatalf("status = %s, want queued", st)
	}
	f.pass(t)
	if n := len(f.mb.sentFrames()); n != 1 {
		t.Fatalf("queued item resent: %d sends", n)
	}
}

func TestStalePrekeyReseal(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	oldPK := f.gpu.prekey
	id := f.send(t, "sealed to the old prekey")
	f.pass(t)
	if fr, _, err := openAs(t, f.me, f.gpu, f.mb.sentFrames()[0].Frame, oldPK); err != nil || fr.Header.PKID != oldPK.ID {
		t.Fatalf("first send: pk %s err %v", fr.Header.PKID, err)
	}

	// The peer rotated and purged oldPK; its control.stale_prekey carried newPK.
	newPK, err := keys.GeneratePrekey(testEpoch.Add(8 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	p := mustGetPeer(t, f.peers, f.gpu.rec.MachineID)
	p.Prekey = newPK.Signed(f.gpu.id).Wire()
	mustPut(t, f.peers, p)

	if err := f.o.Reseal(ctx, f.gpu.rec.MachineID, id); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	sent := f.mb.sentFrames()
	if len(sent) != 2 || sent[1].ID != id {
		t.Fatalf("resend = %+v", sent)
	}
	fr, env, err := openAs(t, f.me, f.gpu, sent[1].Frame, newPK) // old key is gone on the peer
	if err != nil {
		t.Fatalf("peer cannot open the resealed frame: %v", err)
	}
	if fr.Header.PKID != newPK.ID || env.ID != id {
		t.Fatalf("resealed to %s id %s, want %s id %s", fr.Header.PKID, env.ID, newPK.ID, id)
	}
	if _, _, err := openAs(t, f.me, f.gpu, sent[1].Frame, oldPK); !errors.Is(err, sealing.ErrUnknownPrekey) {
		t.Fatalf("resealed frame still uses the old prekey: %v", err)
	}

	other := newTestPeer(t, "other", core.TrustAskFirst)
	mustPut(t, f.peers, other.rec)
	if err := f.o.Reseal(ctx, other.rec.MachineID, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("reseal by a different peer err = %v, want ErrNotFound", err)
	}
}

func TestOutboundStatusHandling(t *testing.T) {
	cases := []struct {
		name         string
		status       transport.SendStatus
		sendErr      error
		wantPresent  bool
		wantStatus   store.OutboxStatus
		wantNext     time.Duration
		wantErrors   int
		pausedByPeer bool
	}{
		{name: "queued", status: transport.SendQueued, wantPresent: true, wantStatus: store.OutboxQueued},
		{name: "not allowed", status: transport.SendNotAllowed, wantPresent: true, wantStatus: store.OutboxHeld, pausedByPeer: true},
		{name: "queue full", status: transport.SendQueueFull, wantPresent: true, wantStatus: store.OutboxPending, wantNext: time.Second, wantErrors: 1},
		{name: "rate limited", status: transport.SendRateLimited, wantPresent: true, wantStatus: store.OutboxPending, wantNext: time.Second},
		{name: "transport error", sendErr: errBoom, wantPresent: true, wantStatus: store.OutboxPending, wantNext: time.Second},
		{name: "too large", status: transport.SendTooLarge, wantErrors: 1},
		{name: "unknown mailbox", status: transport.SendUnknownMailbox, wantErrors: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOutboundFixture(t)
			f.mb.statuses = []transport.SendStatus{tc.status}
			f.mb.sendErr = tc.sendErr
			id := f.send(t, "x")
			f.pass(t)
			it, ok := f.outbox.item(id)
			if ok != tc.wantPresent {
				t.Fatalf("present = %v, want %v", ok, tc.wantPresent)
			}
			if ok && it.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", it.Status, tc.wantStatus)
			}
			if tc.wantNext > 0 && !it.NextAttempt.Equal(testEpoch.Add(tc.wantNext)) {
				t.Fatalf("next = %v, want +%v", it.NextAttempt.Sub(testEpoch), tc.wantNext)
			}
			if got := len(f.o.Errors()); got != tc.wantErrors {
				t.Fatalf("errors = %v", f.o.Errors())
			}
			if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).PausedByPeer; got != tc.pausedByPeer {
				t.Fatalf("PausedByPeer = %v", got)
			}
		})
	}
}

func TestOutboundBackoffDoublesToMax(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 300, 300}
	for i, w := range want {
		if got := backoffDelay(i + 1); got != w*time.Second {
			t.Errorf("backoffDelay(%d) = %v, want %v", i+1, got, w*time.Second)
		}
	}
	f := newOutboundFixture(t)
	f.mb.statuses = []transport.SendStatus{transport.SendQueueFull, transport.SendQueueFull}
	id := f.send(t, "x")
	f.pass(t)
	f.pass(t) // not due yet
	if n := len(f.mb.sentFrames()); n != 1 {
		t.Fatalf("retried before backoff elapsed: %d sends", n)
	}
	f.clock.Advance(time.Second)
	f.pass(t)
	it := f.status(t, id)
	if it.Attempts != 2 || !it.NextAttempt.Equal(f.clock.Now().Add(2*time.Second)) {
		t.Fatalf("after 2 failures: attempts %d next +%v", it.Attempts, it.NextAttempt.Sub(f.clock.Now()))
	}
}

func TestOutboundPausedPeer(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	p := f.gpu.rec
	p.Paused = true
	mustPut(t, f.peers, p)
	if _, err := f.o.SendEnvelope(ctx, p.MachineID, core.KindChat, "", "", core.ChatBody{Text: "x"}); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("send to paused peer err = %v, want ErrPaused", err)
	}
	if _, err := f.o.SendEnvelope(ctx, p.MachineID, core.KindControlResumed, "", "", core.EmptyBody{}); err != nil {
		t.Fatalf("control kinds must still enqueue: %v", err)
	}
}

func TestOutboundPausedByPeerEnqueuesHeld(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	p := f.gpu.rec
	p.PausedByPeer = true
	mustPut(t, f.peers, p)
	id := f.send(t, "wait for me")
	if st := f.status(t, id).Status; st != store.OutboxHeld {
		t.Fatalf("status = %s, want held", st)
	}
	f.pass(t)
	if len(f.mb.sentFrames()) != 0 {
		t.Fatal("sent to a peer that paused us")
	}
	p.PausedByPeer = false
	mustPut(t, f.peers, p)
	if err := f.o.Release(ctx, p.MachineID); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	if len(f.mb.sentFrames()) != 1 {
		t.Fatal("held item not sent after release")
	}
}

func TestOutboundOfflineAndKilled(t *testing.T) {
	f := newOutboundFixture(t)
	f.slot.set(nil)
	id := f.send(t, "x")
	f.pass(t)
	if f.status(t, id).Status != store.OutboxPending {
		t.Fatal("offline pass changed the item")
	}
	if err := f.o.SendDirect(context.Background(), f.gpu.rec, core.KindControlPaused, core.EmptyBody{}); !errors.Is(err, ErrOffline) {
		t.Fatalf("SendDirect offline err = %v", err)
	}
	f.slot.set(f.mb)
	f.killed = true
	f.pass(t)
	if len(f.mb.sentFrames()) != 0 {
		t.Fatal("sent while killed")
	}
	// While killed, envelopes (such as task.update expired) are queued, not
	// dropped; nothing leaves until resume. SendDirect bypasses the outbox and
	// is refused.
	late, err := f.o.SendEnvelope(context.Background(), f.gpu.rec.MachineID, core.KindTaskUpdate, "", "", core.TaskUpdateBody{TaskID: "T", State: core.TaskExpired})
	if err != nil {
		t.Fatalf("SendEnvelope while killed err = %v", err)
	}
	if f.status(t, late).Status != store.OutboxPending {
		t.Fatal("envelope enqueued while killed is not pending")
	}
	if err := f.o.SendDirect(context.Background(), f.gpu.rec, core.KindControlPaused, core.EmptyBody{}); !errors.Is(err, core.ErrKilled) {
		t.Fatalf("SendDirect while killed err = %v", err)
	}
	f.pass(t)
	if len(f.mb.sentFrames()) != 0 {
		t.Fatal("sent while killed")
	}
	f.killed = false
	f.pass(t)
	if n := len(f.mb.sentFrames()); n != 2 {
		t.Fatalf("sent %d after resume, want 2", n)
	}
}

func TestOutboundUnknownPeer(t *testing.T) {
	f := newOutboundFixture(t)
	if _, err := f.o.SendEnvelope(context.Background(), core.MachineID("nobody"), core.KindChat, "", "", core.ChatBody{}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestOutboundBadPrekeyBacksOff(t *testing.T) {
	f := newOutboundFixture(t)
	p := f.gpu.rec
	p.Prekey.Sig = []byte("forged")
	mustPut(t, f.peers, p)
	id := f.send(t, "x")
	f.pass(t)
	if len(f.mb.sentFrames()) != 0 {
		t.Fatal("sealed to an unverified prekey")
	}
	if it := f.status(t, id); it.Status != store.OutboxPending || it.Attempts != 1 {
		t.Fatalf("item = %+v", it)
	}
	if len(f.o.Errors()) != 1 {
		t.Fatalf("errors = %v", f.o.Errors())
	}
}

func TestOutboundMarkDeliveredOnlyForSender(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	other := newTestPeer(t, "other", core.TrustAskFirst)
	mustPut(t, f.peers, other.rec)
	mine := f.send(t, "to gpu")
	theirs, err := f.o.SendEnvelope(ctx, other.rec.MachineID, core.KindChat, "", "", core.ChatBody{Text: "to other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.o.MarkDelivered(ctx, f.gpu.rec.MachineID, []string{mine, theirs, "unknown"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.outbox.item(mine); ok {
		t.Fatal("delivered item not deleted")
	}
	if _, ok := f.outbox.item(theirs); !ok {
		t.Fatal("a peer deleted an item addressed to someone else")
	}
}

func TestOutboundSendDirect(t *testing.T) {
	f := newOutboundFixture(t)
	if err := f.o.SendDirect(context.Background(), f.gpu.rec, core.KindControlUnpaired, core.EmptyBody{}); err != nil {
		t.Fatal(err)
	}
	sent := f.mb.sentFrames()
	if len(sent) != 1 || f.outbox.len() != 0 {
		t.Fatalf("sent %d, outbox %d", len(sent), f.outbox.len())
	}
	_, env, err := openAs(t, f.me, f.gpu, sent[0].Frame, f.gpu.prekey)
	if err != nil || env.Kind != core.KindControlUnpaired {
		t.Fatalf("env %+v err %v", env, err)
	}
	f.mb.statuses = []transport.SendStatus{transport.SendNotAllowed}
	if err := f.o.SendDirect(context.Background(), f.gpu.rec, core.KindControlPaused, core.EmptyBody{}); err == nil {
		t.Fatal("refused direct send reported success")
	}
}

func TestOutboundPurgeOld(t *testing.T) {
	f := newOutboundFixture(t)
	id := f.send(t, "old")
	f.clock.Advance(core.OutboxRetention + time.Minute)
	fresh := f.send(t, "new")
	n, err := f.o.PurgeOld(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	if _, ok := f.outbox.item(id); ok {
		t.Fatal("old item kept")
	}
	if _, ok := f.outbox.item(fresh); !ok {
		t.Fatal("fresh item purged")
	}
}

func TestOutboundRunWakesOnSend(t *testing.T) {
	f := newOutboundFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.o.Run(ctx) }()
	f.send(t, "wake up")
	deadline := time.After(2 * time.Second)
	for len(f.mb.sentFrames()) == 0 {
		select {
		case <-deadline:
			t.Fatal("Run did not send after wake")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOutboundRejectsOversizedEnvelope(t *testing.T) {
	f := newOutboundFixture(t)
	big := strings.Repeat("a", core.MaxFrameBytes+1)
	_, err := f.o.SendEnvelope(context.Background(), f.gpu.rec.MachineID, core.KindChat, "", "", core.ChatBody{Text: big})
	if !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("SendEnvelope = %v, want ErrTooLarge", err)
	}
	if n := f.outbox.len(); n != 0 {
		t.Fatalf("outbox has %d items after a refused send", n)
	}
}

func TestOutboundDropsItemThatSealsTooLarge(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	env, err := core.NewEnvelope(f.clock, f.me.MachineID(), f.gpu.rec.MachineID, core.KindChat,
		core.ChatBody{Text: strings.Repeat("a", core.MaxFrameBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	now := f.clock.Now()
	if err := f.outbox.Enqueue(ctx, store.OutboxItem{ID: env.ID, To: env.ToMachine, Envelope: raw,
		Status: store.OutboxPending, NextAttempt: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f.pass(t)
	if len(f.mb.sentFrames()) != 0 {
		t.Fatal("sent an oversized frame")
	}
	if _, ok := f.outbox.item(env.ID); ok {
		t.Fatal("oversized item kept (would back off forever)")
	}
	if len(f.o.Errors()) != 1 {
		t.Fatalf("errors = %v", f.o.Errors())
	}
}

func TestOutboundControlItemsAreNeverHeld(t *testing.T) {
	f := newOutboundFixture(t)
	ctx := context.Background()
	p := f.gpu.rec
	p.PausedByPeer = true
	mustPut(t, f.peers, p)
	id, err := f.o.SendEnvelope(ctx, p.MachineID, core.KindControlResumed, "", "", core.EmptyBody{})
	if err != nil {
		t.Fatal(err)
	}
	if st := f.status(t, id).Status; st != store.OutboxPending {
		t.Fatalf("control item status = %s, want pending", st)
	}
	f.mb.statuses = []transport.SendStatus{transport.SendNotAllowed}
	f.pass(t)
	it := f.status(t, id)
	if it.Status != store.OutboxPending || it.Attempts != 1 || !it.NextAttempt.Equal(testEpoch.Add(time.Second)) {
		t.Fatalf("control item after not_allowed = %+v, want pending with backoff", it)
	}
	f.clock.Advance(time.Second)
	f.pass(t)
	if n := len(f.mb.sentFrames()); n != 2 {
		t.Fatalf("control item not retried: %d sends", n)
	}
	if _, ok := f.outbox.item(id); ok {
		t.Fatal("control item kept after the relay queued it")
	}
}

// The relay drops a queued frame after core.RelayTTL; without a control.delivered by
// then the item is sent again.
func TestOutboundResendsQueuedItemAfterRelayTTL(t *testing.T) {
	f := newOutboundFixture(t)
	id := f.send(t, "are you there")
	f.pass(t)
	if it := f.status(t, id); it.Status != store.OutboxQueued || !it.NextAttempt.Equal(testEpoch.Add(core.RelayTTL)) {
		t.Fatalf("after send: %+v, want queued until +RelayTTL", it)
	}
	f.clock.Advance(core.RelayTTL - time.Minute)
	f.pass(t)
	if n := len(f.mb.sentFrames()); n != 1 {
		t.Fatalf("resent before the relay TTL: %d sends", n)
	}
	f.clock.Advance(time.Minute)
	f.pass(t)
	sent := f.mb.sentFrames()
	if len(sent) != 2 || sent[1].ID != id {
		t.Fatalf("not resent after the relay TTL: %+v", sent)
	}
	if st := f.status(t, id).Status; st != store.OutboxQueued {
		t.Fatalf("status after resend = %s", st)
	}
}
