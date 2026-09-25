package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

type inboundFixture struct {
	in       *Inbound
	me       *keys.Identity
	peers    *memPeers
	prekeys  *PrekeyManager
	myPK     keys.SignedPrekey
	sender   *recordingSender
	registry *HandlerRegistry
	clock    *core.FakeClock
	dedup    *memDedup
	gpu      testPeer

	mu      sync.Mutex
	handled []string
	failing map[string]error // envelope ID -> error returned once by the chat handler
	killed  atomic.Bool
}

func newInboundFixture(t *testing.T) *inboundFixture {
	t.Helper()
	me, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	f := &inboundFixture{me: me, peers: newMemPeers(), sender: &recordingSender{},
		registry: NewHandlerRegistry(), clock: core.NewFakeClock(testEpoch), failing: map[string]error{}}
	f.prekeys = NewPrekeyManager(newMemPrekeys(), f.peers, me, f.sender, f.clock)
	if f.myPK, err = f.prekeys.EnsureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.registry.Register(core.KindChat, HandlerFunc(func(_ context.Context, _ store.Peer, env core.Envelope) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if err, ok := f.failing[env.ID]; ok {
			delete(f.failing, env.ID)
			return err
		}
		f.handled = append(f.handled, env.ID)
		return nil
	}))
	f.dedup = newMemDedup()
	f.in = NewInbound(me, f.peers, f.dedup, f.prekeys, f.registry, f.sender, f.clock, f.killed.Load, nil)
	f.gpu = newTestPeer(t, "gpu-box", core.TrustAskFirst)
	mustPut(t, f.peers, f.gpu.rec)
	return f
}

func (f *inboundFixture) handledIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.handled...)
}

// frameFrom seals a chat from p to us at time `at`, addressed to prekey `to`.
func (f *inboundFixture) frameFrom(t *testing.T, p testPeer, at time.Time, to keys.SignedPrekey) (string, []byte) {
	t.Helper()
	env, err := core.NewEnvelope(core.NewFakeClock(at), p.id.MachineID(), f.me.MachineID(), core.KindChat, core.ChatBody{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := sealing.Seal(p.id, to, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return env.ID, raw
}

// run feeds deliveries to Inbound.Run and waits for it to finish.
func (f *inboundFixture) run(t *testing.T, ds ...transport.Delivery) *fakeMailbox {
	t.Helper()
	mb, err := f.runErr(ds...)
	if err != nil {
		t.Fatal(err)
	}
	return mb
}

// runErr is run that returns Run's error instead of failing the test.
func (f *inboundFixture) runErr(ds ...transport.Delivery) (*fakeMailbox, error) {
	mb := newFakeMailbox(nil)
	for _, d := range ds {
		mb.deliveries <- d
	}
	close(mb.deliveries)
	return mb, f.in.Run(context.Background(), mb)
}

func deliveredIDs(t *testing.T, s *recordingSender) []string {
	t.Helper()
	var ids []string
	for _, e := range s.ofKind(core.KindControlDelivered) {
		var b core.DeliveredBody
		if err := json.Unmarshal(e.Body, &b); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.IDs...)
	}
	return ids
}

func TestInboundDeliversAndConfirms(t *testing.T) {
	f := newInboundFixture(t)
	id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
	mb := f.run(t, transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw})
	if got := f.handledIDs(); !slices.Equal(got, []string{id}) {
		t.Fatalf("handled = %v", got)
	}
	if got := mb.ackedSeqs(); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("acks = %v", got)
	}
	if got := deliveredIDs(t, f.sender); !slices.Equal(got, []string{id}) {
		t.Fatalf("delivered receipt ids = %v", got)
	}
	if e := f.sender.ofKind(core.KindControlDelivered)[0]; e.To != f.gpu.rec.MachineID {
		t.Fatalf("receipt sent to %s", e.To)
	}
}

func TestDuplicateDeliveryShownOnce(t *testing.T) {
	f := newInboundFixture(t)
	id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
	// The sender resent after a lost sent{queued}; the relay queued both copies.
	mb := f.run(t,
		transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw},
		transport.Delivery{Seq: 2, From: f.gpu.id.Public(), ID: id, Frame: raw},
	)
	if got := f.handledIDs(); !slices.Equal(got, []string{id}) {
		t.Fatalf("handler ran %d times: %v", len(got), got)
	}
	if got := mb.ackedSeqs(); !slices.Equal(got, []uint64{1, 2}) {
		t.Fatalf("acks = %v, want both", got)
	}
	if got := deliveredIDs(t, f.sender); len(got) == 0 || !slices.Contains(got, id) {
		t.Fatalf("delivered receipt ids = %v", got)
	}

	// A later redelivery (new connection) is still shown once but confirmed again.
	f.sender = &recordingSender{}
	f.in.sender = f.sender
	f.run(t, transport.Delivery{Seq: 3, From: f.gpu.id.Public(), ID: id, Frame: raw})
	if n := len(f.handledIDs()); n != 1 {
		t.Fatalf("handler ran %d times after redelivery", n)
	}
	if got := deliveredIDs(t, f.sender); !slices.Equal(got, []string{id}) {
		t.Fatalf("redelivery not re-confirmed: %v", got)
	}
}

func TestInboundDrops(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, f *inboundFixture) transport.Delivery
	}{
		{"unknown sender", func(t *testing.T, f *inboundFixture) transport.Delivery {
			stranger := newTestPeer(t, "stranger", core.TrustAskFirst)
			id, raw := f.frameFrom(t, stranger, testEpoch, f.myPK)
			return transport.Delivery{Seq: 1, From: stranger.id.Public(), ID: id, Frame: raw}
		}},
		{"paused peer", func(t *testing.T, f *inboundFixture) transport.Delivery {
			p := f.gpu.rec
			p.Paused = true
			mustPut(t, f.peers, p)
			id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
			return transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
		}},
		{"bad signature", func(t *testing.T, f *inboundFixture) transport.Delivery {
			id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
			fr, err := sealing.ParseFrame(raw)
			if err != nil {
				t.Fatal(err)
			}
			fr.Sig[0] ^= 0xff
			raw, _ = fr.Marshal()
			return transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
		}},
		{"signed by another key", func(t *testing.T, f *inboundFixture) transport.Delivery {
			impostor := newTestPeer(t, "impostor", core.TrustAskFirst)
			id, raw := f.frameFrom(t, impostor, testEpoch, f.myPK)
			return transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
		}},
		{"garbage frame", func(t *testing.T, f *inboundFixture) transport.Delivery {
			return transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: "x", Frame: []byte("not json")}
		}},
		{"too old", func(t *testing.T, f *inboundFixture) transport.Delivery {
			id, raw := f.frameFrom(t, f.gpu, testEpoch.Add(-core.MaxMessageAge-time.Minute), f.myPK)
			return transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
		}},
		{"from the future", func(t *testing.T, f *inboundFixture) transport.Delivery {
			id, raw := f.frameFrom(t, f.gpu, testEpoch.Add(core.MaxClockSkew+time.Minute), f.myPK)
			return transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
		}},
		{"no handler for kind", func(t *testing.T, f *inboundFixture) transport.Delivery {
			env, err := core.NewEnvelope(f.clock, f.gpu.id.MachineID(), f.me.MachineID(), core.KindFileOffer, core.EmptyBody{})
			if err != nil {
				t.Fatal(err)
			}
			fr, err := sealing.Seal(f.gpu.id, f.myPK, env)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := fr.Marshal()
			return transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: env.ID, Frame: raw}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInboundFixture(t)
			d := tc.build(t, f)
			mb := f.run(t, d)
			if n := len(f.handledIDs()); n != 0 {
				t.Fatalf("handler ran %d times", n)
			}
			if got := mb.ackedSeqs(); !slices.Equal(got, []uint64{1}) {
				t.Fatalf("dropped delivery must still be acked, acks = %v", got)
			}
			if f.in.Dropped() != 1 {
				t.Fatalf("Dropped = %d", f.in.Dropped())
			}
			if n := len(f.sender.envelopes()); n != 0 {
				t.Fatalf("sent %d envelopes for a dropped delivery", n)
			}
		})
	}
}

func TestInboundFutureTimestampCountsSkew(t *testing.T) {
	f := newInboundFixture(t)
	id, raw := f.frameFrom(t, f.gpu, testEpoch.Add(core.MaxClockSkew+time.Minute), f.myPK)
	f.run(t, transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw})
	if f.in.SkewRejected() != 1 {
		t.Fatalf("SkewRejected = %d", f.in.SkewRejected())
	}
	// Within the allowed skew is fine.
	id2, raw2 := f.frameFrom(t, f.gpu, testEpoch.Add(core.MaxClockSkew-time.Minute), f.myPK)
	f.run(t, transport.Delivery{Seq: 2, From: f.gpu.id.Public(), ID: id2, Frame: raw2})
	if got := f.handledIDs(); !slices.Equal(got, []string{id2}) {
		t.Fatalf("handled = %v", got)
	}
}

func TestInboundStalePrekeyReply(t *testing.T) {
	f := newInboundFixture(t)
	gone, err := keys.GeneratePrekey(testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	id, raw := f.frameFrom(t, f.gpu, testEpoch, gone.Signed(f.me)) // never stored, like a purged key
	mb := f.run(t, transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw})
	if len(f.handledIDs()) != 0 {
		t.Fatal("handled a frame sealed to an unknown prekey")
	}
	if got := mb.ackedSeqs(); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("acks = %v", got)
	}
	sent := f.sender.ofKind(core.KindControlStalePrekey)
	if len(sent) != 1 || sent[0].To != f.gpu.rec.MachineID {
		t.Fatalf("stale_prekey sent = %+v", sent)
	}
	var b core.StalePrekeyBody
	if err := json.Unmarshal(sent[0].Body, &b); err != nil {
		t.Fatal(err)
	}
	if b.MsgID != id || b.Prekey.ID != f.myPK.ID {
		t.Fatalf("body = %+v, want msg %s prekey %s", b, id, f.myPK.ID)
	}
	if err := keys.SignedPrekeyFromWire(b.Prekey).Verify(f.me.Public()); err != nil {
		t.Fatalf("reply prekey not signed by us: %v", err)
	}

	// The resend sealed to the current prekey must not be treated as a duplicate.
	id2 := id
	env, _ := core.NewEnvelope(f.clock, f.gpu.id.MachineID(), f.me.MachineID(), core.KindChat, core.ChatBody{Text: "hi"})
	env.ID = id2
	fr, err := sealing.Seal(f.gpu.id, f.myPK, env)
	if err != nil {
		t.Fatal(err)
	}
	raw2, _ := fr.Marshal()
	f.run(t, transport.Delivery{Seq: 2, From: f.gpu.id.Public(), ID: id2, Frame: raw2})
	if got := f.handledIDs(); !slices.Equal(got, []string{id}) {
		t.Fatalf("resealed resend handled = %v", got)
	}
}

func TestInboundRetryableErrorNotAcked(t *testing.T) {
	f := newInboundFixture(t)
	id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
	id2, raw2 := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
	f.failing[id] = Retryable(errBoom)
	mb, err := f.runErr(
		transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw},
		transport.Delivery{Seq: 2, From: f.gpu.id.Public(), ID: id2, Frame: raw2},
	)
	if !errors.Is(err, ErrRetryLater) {
		t.Fatalf("Run err = %v, want ErrRetryLater so the connection is recycled", err)
	}
	if got := mb.ackedSeqs(); len(got) != 0 {
		t.Fatalf("acks = %v: a cumulative ack would drop the failed delivery", got)
	}
	if got := deliveredIDs(t, f.sender); len(got) != 0 {
		t.Fatalf("confirmed %v before the handler succeeded", got)
	}
	// Reconnect: the relay redelivers both from seq 1.
	mb = f.run(t,
		transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw},
		transport.Delivery{Seq: 2, From: f.gpu.id.Public(), ID: id2, Frame: raw2},
	)
	if got := f.handledIDs(); !slices.Equal(got, []string{id, id2}) {
		t.Fatalf("handled after redelivery = %v", got)
	}
	if got := mb.ackedSeqs(); !slices.Equal(got, []uint64{1, 2}) {
		t.Fatalf("acks after redelivery = %v", got)
	}
}

func TestInboundRetryableFailureSurvivesRestart(t *testing.T) {
	f := newInboundFixture(t)
	id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
	f.failing[id] = Retryable(errBoom)
	d := transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
	if _, err := f.runErr(d); !errors.Is(err, ErrRetryLater) {
		t.Fatalf("Run err = %v", err)
	}
	if got := deliveredIDs(t, f.sender); len(got) != 0 {
		t.Fatalf("receipt sent for a failed delivery: %v", got)
	}
	// Daemon restart: a fresh Inbound over the same dedup store.
	f.sender = &recordingSender{}
	f.in = NewInbound(f.me, f.peers, f.dedup, f.prekeys, f.registry, f.sender, f.clock, f.killed.Load, nil)
	f.run(t, d)
	if got := f.handledIDs(); !slices.Equal(got, []string{id}) {
		t.Fatalf("handled after restart = %v: the retried message was lost", got)
	}
	if got := deliveredIDs(t, f.sender); !slices.Equal(got, []string{id}) {
		t.Fatalf("receipt after success = %v", got)
	}
}

func TestInboundPlainHandlerErrorStillAckedAndConfirmed(t *testing.T) {
	f := newInboundFixture(t)
	id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
	f.failing[id] = errBoom
	d := transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
	mb := f.run(t, d)
	if got := mb.ackedSeqs(); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("acks = %v", got)
	}
	// The message was received; the sender must stop resending it.
	if got := deliveredIDs(t, f.sender); !slices.Equal(got, []string{id}) {
		t.Fatalf("delivered receipt ids = %v", got)
	}
	f.run(t, d)
	if n := len(f.handledIDs()); n != 0 {
		t.Fatalf("non-retryable failure handled again on redelivery (%d)", n)
	}
}

func TestInboundStalePrekeyReplyOncePerMessage(t *testing.T) {
	f := newInboundFixture(t)
	gone, err := keys.GeneratePrekey(testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	id, raw := f.frameFrom(t, f.gpu, testEpoch, gone.Signed(f.me))
	id2, raw2 := f.frameFrom(t, f.gpu, testEpoch, gone.Signed(f.me))
	d := transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw}
	f.run(t, d, d, transport.Delivery{Seq: 2, From: f.gpu.id.Public(), ID: id2, Frame: raw2})
	f.run(t, d)
	sent := f.sender.ofKind(core.KindControlStalePrekey)
	if len(sent) != 2 {
		t.Fatalf("stale_prekey replies = %d, want one per message ID", len(sent))
	}
}

func TestInboundControlKindsGetNoReceipt(t *testing.T) {
	f := newInboundFixture(t)
	var got []core.Kind
	f.registry.Register(core.KindControlPaused, HandlerFunc(func(_ context.Context, _ store.Peer, env core.Envelope) error {
		got = append(got, env.Kind)
		return nil
	}))
	env, err := core.NewEnvelope(f.clock, f.gpu.id.MachineID(), f.me.MachineID(), core.KindControlPaused, core.EmptyBody{})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := sealing.Seal(f.gpu.id, f.myPK, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := fr.Marshal()
	f.run(t, transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: env.ID, Frame: raw})
	if len(got) != 1 {
		t.Fatalf("control handler ran %d times", len(got))
	}
	if n := len(f.sender.envelopes()); n != 0 {
		t.Fatalf("control message produced %d envelopes (receipt loop)", n)
	}
}

func TestInboundStopsOnContextCancel(t *testing.T) {
	f := newInboundFixture(t)
	mb := newFakeMailbox(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.in.Run(ctx, mb) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run ignored cancellation")
	}
}

// While the kill switch is on nothing is handled: the delivery is not acked
// (the relay redelivers it after resume) and Run stops.
func TestInboundStopsWhenKilled(t *testing.T) {
	f := newInboundFixture(t)
	f.killed.Store(true)
	id, raw := f.frameFrom(t, f.gpu, testEpoch, f.myPK)
	mb, err := f.runErr(transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: id, Frame: raw})
	if !errors.Is(err, core.ErrKilled) {
		t.Fatalf("Run err = %v, want ErrKilled", err)
	}
	if got := f.handledIDs(); len(got) != 0 {
		t.Fatalf("handled while killed: %v", got)
	}
	if got := mb.ackedSeqs(); len(got) != 0 {
		t.Fatalf("acked while killed: %v", got)
	}
	if seen, _ := f.dedup.Seen(context.Background(), id); seen {
		t.Fatal("marked seen while killed")
	}
}
