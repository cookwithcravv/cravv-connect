package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
)

type prekeyFixture struct {
	m      *PrekeyManager
	store  *memPrekeys
	peers  *memPeers
	sender *recordingSender
	clock  *core.FakeClock
	id     *keys.Identity
}

func newPrekeyFixture(t *testing.T) *prekeyFixture {
	t.Helper()
	id, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	f := &prekeyFixture{store: newMemPrekeys(), peers: newMemPeers(), sender: &recordingSender{},
		clock: core.NewFakeClock(testEpoch), id: id}
	f.m = NewPrekeyManager(f.store, f.peers, id, f.sender, f.clock)
	return f
}

func TestPrekeyEnsureCurrentIsIdempotent(t *testing.T) {
	f := newPrekeyFixture(t)
	ctx := context.Background()
	a, err := f.m.EnsureCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.m.EnsureCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("EnsureCurrent generated twice: %s then %s", a.ID, b.ID)
	}
	if err := a.Verify(f.id.Public()); err != nil {
		t.Fatalf("current prekey not signed by our identity: %v", err)
	}
	cur, err := f.m.Current(ctx)
	if err != nil || cur.ID != a.ID {
		t.Fatalf("Current = %v, %v", cur.ID, err)
	}
	if _, ok := f.m.PrivatePrekey(a.ID); !ok {
		t.Fatal("PrivatePrekey cannot resolve the current prekey")
	}
	if _, ok := f.m.PrivatePrekey("nope"); ok {
		t.Fatal("unknown id resolved")
	}
}

func TestPrekeyRotateIfDue(t *testing.T) {
	f := newPrekeyFixture(t)
	ctx := context.Background()
	peerA := newTestPeer(t, "a")
	peerB := newTestPeer(t, "b")
	paused := newTestPeer(t, "p")
	paused.rec.Paused = true
	for _, p := range []testPeer{peerA, peerB, paused} {
		mustPut(t, f.peers, p.rec)
	}
	first, err := f.m.EnsureCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}

	f.clock.Advance(core.PrekeyRotation - time.Minute)
	rotated, err := f.m.RotateIfDue(ctx)
	if err != nil || rotated {
		t.Fatalf("rotated early: %v %v", rotated, err)
	}

	f.clock.Advance(time.Minute)
	rotated, err = f.m.RotateIfDue(ctx)
	if err != nil || !rotated {
		t.Fatalf("did not rotate when due: %v %v", rotated, err)
	}
	second, err := f.m.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("current prekey unchanged after rotation")
	}
	if _, ok := f.m.PrivatePrekey(first.ID); !ok {
		t.Fatal("superseded prekey must stay resolvable until purge")
	}
	sent := f.sender.ofKind(core.KindControlPrekey)
	if len(sent) != 2 {
		t.Fatalf("control.prekey sent %d times, want 2 (paused peer skipped)", len(sent))
	}
	for _, s := range sent {
		if s.To == paused.rec.MachineID {
			t.Fatal("control.prekey sent to a paused peer")
		}
		var body core.PrekeyBody
		if err := json.Unmarshal(s.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Prekey.ID != second.ID {
			t.Fatalf("broadcast prekey %s, want %s", body.Prekey.ID, second.ID)
		}
		if err := keys.SignedPrekeyFromWire(body.Prekey).Verify(f.id.Public()); err != nil {
			t.Fatalf("broadcast prekey signature: %v", err)
		}
	}
}

func TestPrekeyRotateGeneratesWhenMissing(t *testing.T) {
	f := newPrekeyFixture(t)
	rotated, err := f.m.RotateIfDue(context.Background())
	if err != nil || !rotated {
		t.Fatalf("RotateIfDue with no prekey: %v %v", rotated, err)
	}
	if _, err := f.m.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPrekeyPurgeAfterRetention(t *testing.T) {
	f := newPrekeyFixture(t)
	ctx := context.Background()
	first, err := f.m.EnsureCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(core.PrekeyRotation)
	if _, err := f.m.RotateIfDue(ctx); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(core.PrekeyRetention - time.Hour)
	if n, err := f.m.Purge(ctx); err != nil || n != 0 {
		t.Fatalf("purged %d before retention: %v", n, err)
	}
	f.clock.Advance(2 * time.Hour)
	if n, err := f.m.Purge(ctx); err != nil || n != 1 {
		t.Fatalf("purged %d after retention, want 1: %v", n, err)
	}
	if _, ok := f.m.PrivatePrekey(first.ID); ok {
		t.Fatal("purged prekey still resolvable")
	}
	if _, err := f.m.Current(ctx); err != nil {
		t.Fatalf("current prekey lost by purge: %v", err)
	}
}
