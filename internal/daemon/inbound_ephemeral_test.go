package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

// sealKind seals an envelope of any kind from p to the fixture's machine.
func (f *inboundFixture) sealKind(t *testing.T, p testPeer, kind core.Kind, at time.Time, body any) (string, []byte) {
	t.Helper()
	env, err := core.NewEnvelope(core.NewFakeClock(at), p.id.MachineID(), f.me.MachineID(), kind, body)
	if err != nil {
		t.Fatal(err)
	}
	fr, err := sealing.Seal(p.id, f.myPK, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return env.ID, raw
}

// Ephemeral frames are handled when fresh, dropped when older than
// core.PresenceMaxAge, and never deduplicated or confirmed.
func TestInboundEphemeralFrames(t *testing.T) {
	f := newInboundFixture(t)
	var handled []string
	f.registry.Register(core.KindPresencePing, HandlerFunc(func(_ context.Context, _ store.Peer, env core.Envelope) error {
		handled = append(handled, env.ID)
		return nil
	}))
	body := core.PresencePingBody{TS: 1, LinkIDs: []string{}}
	fresh, rawFresh := f.sealKind(t, f.gpu, core.KindPresencePing, testEpoch.Add(-core.PresenceMaxAge), body)
	_, rawStale := f.sealKind(t, f.gpu, core.KindPresencePing, testEpoch.Add(-core.PresenceMaxAge-time.Millisecond), body)
	mb := f.run(t,
		transport.Delivery{Seq: 1, From: f.gpu.id.Public(), ID: "a", Frame: rawFresh},
		transport.Delivery{Seq: 2, From: f.gpu.id.Public(), ID: "b", Frame: rawStale},
		transport.Delivery{Seq: 3, From: f.gpu.id.Public(), ID: "c", Frame: rawFresh}, // a relay redelivery
	)
	if !slices.Equal(handled, []string{fresh, fresh}) {
		t.Fatalf("handled %v, want the fresh ping twice (no dedup) and not the stale one", handled)
	}
	if got := mb.ackedSeqs(); !slices.Equal(got, []uint64{1, 2, 3}) {
		t.Fatalf("acks = %v", got)
	}
	if got := deliveredIDs(t, f.sender); len(got) != 0 {
		t.Fatalf("ephemeral frames were confirmed: %v", got)
	}
	if seen, _ := f.dedup.Seen(context.Background(), fresh); seen {
		t.Fatal("an ephemeral frame was marked in the dedup store")
	}
}

func TestOutboxRefusesEphemeralKinds(t *testing.T) {
	o := NewOutbound(nil, newMemPeers(), newMemOutbox(), &mailboxSlot{}, core.NewFakeClock(testEpoch), nil, nil)
	for _, k := range []core.Kind{core.KindPresencePing, core.KindPresencePong, core.KindSessionsList, core.KindSessionsListed} {
		if _, err := o.SendEnvelope(context.Background(), "m", k, "", core.EmptyBody{}); err == nil {
			t.Errorf("%s went into the outbox", k)
		}
	}
}
