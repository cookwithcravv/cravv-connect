package daemon

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

type peerFixture struct {
	svc   *PeerService
	peers *memPeers
	mb    *fakeMailbox
	slot  *mailboxSlot
	out   *recordingSender
	audit *recordingAudit
	log   *callLog
	gpu   testPeer
}

func newPeerFixture(t *testing.T) *peerFixture {
	t.Helper()
	f := &peerFixture{peers: newMemPeers(), audit: &recordingAudit{}, log: &callLog{}, slot: &mailboxSlot{}}
	f.mb = newFakeMailbox(f.log)
	f.slot.set(f.mb)
	f.out = &recordingSender{log: f.log}
	f.svc = NewPeerService(f.peers, f.slot, f.out, f.audit, core.NewFakeClock(testEpoch))
	f.gpu = newTestPeer(t, "gpu-box", core.TrustAskFirst)
	mustPut(t, f.peers, f.gpu.rec)
	return f
}

type trustRecorder struct{ lowered []string }

func (r *trustRecorder) TrustLowered(_ context.Context, p store.Peer) error {
	r.lowered = append(r.lowered, p.Alias+":"+p.TrustIn.String())
	return nil
}

func TestPeerResolve(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	cases := []struct {
		in      string
		session string
		wantErr bool
	}{
		{"gpu-box", "", false},
		{"GPU-Box", "", false},
		{"gpu-box/claude@proj-2", "claude@proj-2", false},
		{string(f.gpu.rec.MachineID), "", false},
		{"nobody", "", true},
		{"", "", true},
		{"/claude@x", "", true},
	}
	for _, tc := range cases {
		p, session, err := f.svc.Resolve(ctx, tc.in)
		if tc.wantErr {
			if !errors.Is(err, core.ErrNotFound) {
				t.Errorf("Resolve(%q) err = %v, want ErrNotFound", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.in, err)
		}
		if p.MachineID != f.gpu.rec.MachineID || session != tc.session {
			t.Errorf("Resolve(%q) = %s,%q", tc.in, p.Alias, session)
		}
	}
}

func TestPeerSetAlias(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	other := newTestPeer(t, "laptop", core.TrustAskFirst)
	mustPut(t, f.peers, other.rec)

	if err := f.svc.SetAlias(ctx, "gpu-box", "Big GPU!"); err != nil {
		t.Fatal(err)
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).Alias; got != "big-gpu" {
		t.Fatalf("alias = %q, want big-gpu", got)
	}
	if err := f.svc.SetAlias(ctx, "big-gpu", "laptop"); !errors.Is(err, store.ErrAliasTaken) {
		t.Fatalf("taken alias err = %v", err)
	}
	if err := f.svc.SetAlias(ctx, "big-gpu", "!!!"); !errors.Is(err, ErrBadAlias) {
		t.Fatalf("empty alias err = %v", err)
	}
}

func TestPeerSetTrust(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	obs := &trustRecorder{}
	f.svc.AddTrustObserver(obs)

	if err := f.svc.SetTrust(ctx, "gpu-box", core.TrustAutonomous, false); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("raise without unlock err = %v, want ErrAuthRequired", err)
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).TrustIn; got != core.TrustAskFirst {
		t.Fatalf("trust changed to %s without unlock", got)
	}
	if err := f.svc.SetTrust(ctx, "gpu-box", core.TrustAutonomous, true); err != nil {
		t.Fatal(err)
	}
	if len(obs.lowered) != 0 {
		t.Fatalf("observer called on raise: %v", obs.lowered)
	}
	if err := f.svc.SetTrust(ctx, "gpu-box", core.TrustChatOnly, false); err != nil {
		t.Fatalf("lowering must not need unlock: %v", err)
	}
	if got := mustGetPeer(t, f.peers, f.gpu.rec.MachineID).TrustIn; got != core.TrustChatOnly {
		t.Fatalf("trust = %s, want chat-only", got)
	}
	if !reflect.DeepEqual(obs.lowered, []string{"gpu-box:chat-only"}) {
		t.Fatalf("observer = %v", obs.lowered)
	}
	ev := f.audit.last()
	if ev.Type != audit.EvTrust || ev.Detail["from"] != "autonomous" || ev.Detail["to"] != "chat-only" {
		t.Fatalf("audit = %+v", ev)
	}
	if err := f.svc.SetTrust(ctx, "gpu-box", core.TrustLevel(9), true); err == nil {
		t.Fatal("invalid level accepted")
	}
}

func TestPeerPauseSendsNoticeBeforeDeny(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	if err := f.svc.Pause(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	short := f.gpu.rec.MachineID.Short()
	want := []string{
		"direct control.paused " + short,
		"hold " + short,
		"deny " + short,
	}
	if got := f.log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !mustGetPeer(t, f.peers, f.gpu.rec.MachineID).Paused {
		t.Fatal("peer not marked paused")
	}
	if f.audit.last().Type != audit.EvPause {
		t.Fatalf("audit = %v", f.audit.types())
	}
}

func TestPeerPauseOfflineDeniesOnNextConnect(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	f.slot.set(nil)
	if err := f.svc.Pause(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if f.mb.isDenied(f.gpu.rec.IK) {
		t.Fatal("offline pause touched the mailbox")
	}
	if err := f.svc.SyncAllowList(ctx, f.mb); err != nil {
		t.Fatal(err)
	}
	if !f.mb.isDenied(f.gpu.rec.IK) || f.mb.isAllowed(f.gpu.rec.IK) {
		t.Fatal("paused peer not denied on connect")
	}
}

func TestPeerResume(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	if err := f.svc.Pause(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Resume(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	p := mustGetPeer(t, f.peers, f.gpu.rec.MachineID)
	if p.Paused {
		t.Fatal("still paused")
	}
	if !f.mb.isAllowed(p.IK) {
		t.Fatal("not re-allowed")
	}
	if n := len(f.out.ofKind(core.KindControlResumed)); n != 1 {
		t.Fatalf("control.resumed sent %d times", n)
	}
	if len(f.out.released) != 1 {
		t.Fatalf("outbox not released: %v", f.out.released)
	}
	if f.audit.last().Type != audit.EvResume {
		t.Fatalf("audit = %v", f.audit.types())
	}
}

func TestPeerResumeKeepsHoldWhilePausedByPeer(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	if err := f.svc.Pause(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.MarkPausedByPeer(ctx, f.gpu.rec.MachineID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Resume(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if len(f.out.released) != 0 {
		t.Fatal("released outbox although the peer still pauses us")
	}
}

func TestPeerUnpair(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	if err := f.svc.Unpair(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	short := f.gpu.rec.MachineID.Short()
	want := []string{"direct control.unpaired " + short, "deny " + short, "forget " + short}
	if got := f.log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if _, err := f.peers.GetPeer(ctx, f.gpu.rec.MachineID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("peer still stored: %v", err)
	}
	ev := f.audit.last()
	if ev.Type != audit.EvUnpair || ev.Detail["by_peer"] != false {
		t.Fatalf("audit = %+v", ev)
	}
}

func TestPeerUnpairOfflineDeniesOnNextConnect(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	f.slot.set(nil)
	if err := f.svc.Unpair(ctx, "gpu-box"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SyncAllowList(ctx, f.mb); err != nil {
		t.Fatal(err)
	}
	if !f.mb.isDenied(f.gpu.rec.IK) {
		t.Fatal("unpaired peer not denied on next connect")
	}
}

func TestPeerRemoveByPeer(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	if err := f.svc.RemoveByPeer(ctx, f.gpu.rec.MachineID); err != nil {
		t.Fatal(err)
	}
	if len(f.out.direct) != 0 {
		t.Fatal("sent a notice back to a peer that unpaired us")
	}
	if _, err := f.peers.GetPeer(ctx, f.gpu.rec.MachineID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("peer still stored")
	}
	if ev := f.audit.last(); ev.Detail["by_peer"] != true {
		t.Fatalf("audit = %+v", ev)
	}
	if err := f.svc.RemoveByPeer(ctx, f.gpu.rec.MachineID); err != nil {
		t.Fatalf("second removal must be a no-op: %v", err)
	}
}

func TestPeerMarkPausedByPeer(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	if err := f.svc.MarkPausedByPeer(ctx, f.gpu.rec.MachineID, true); err != nil {
		t.Fatal(err)
	}
	if !mustGetPeer(t, f.peers, f.gpu.rec.MachineID).PausedByPeer {
		t.Fatal("flag not set")
	}
	if len(f.out.held) != 1 {
		t.Fatal("outbox not held")
	}
	if err := f.svc.MarkPausedByPeer(ctx, f.gpu.rec.MachineID, false); err != nil {
		t.Fatal(err)
	}
	if mustGetPeer(t, f.peers, f.gpu.rec.MachineID).PausedByPeer {
		t.Fatal("flag not cleared")
	}
}

func TestPeerSyncAllowList(t *testing.T) {
	f := newPeerFixture(t)
	ctx := context.Background()
	paused := newTestPeer(t, "paused", core.TrustAskFirst)
	paused.rec.Paused = true
	mustPut(t, f.peers, paused.rec)
	if err := f.svc.SyncAllowList(ctx, f.mb); err != nil {
		t.Fatal(err)
	}
	if !f.mb.isAllowed(f.gpu.rec.IK) {
		t.Fatal("active peer not allowed")
	}
	if !f.mb.isDenied(paused.rec.IK) {
		t.Fatal("paused peer not denied")
	}
	list, err := f.svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}
}
