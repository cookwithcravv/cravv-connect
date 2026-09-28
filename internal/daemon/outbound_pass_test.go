package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// cutOffRecorder records PeerCutOff calls.
type cutOffRecorder struct{ reasons []string }

func (c *cutOffRecorder) PeerCutOff(_ context.Context, _ store.Peer, reason string) error {
	c.reasons = append(c.reasons, reason)
	return nil
}

// A relay not_allowed long after pairing means the peer paused (or
// unpaired) this machine. Recording it goes through the PeerService, so the
// cut-off observers run: links close and tasks end, as for control.paused.
func TestOutboundNotAllowedCutsThePeerOff(t *testing.T) {
	f := newOutboundFixture(t)
	peers := NewPeerService(f.peers, f.slot, f.o, nil, f.clock)
	cut := &cutOffRecorder{}
	peers.AddCutOffObserver(cut)
	f.o.SetPauseRecorder(peers)
	f.mb.statuses = []transport.SendStatus{transport.SendNotAllowed}
	id := f.send(t, "x")
	f.pass(t)
	if !mustGetPeer(t, f.peers, f.gpu.rec.MachineID).PausedByPeer {
		t.Fatal("peer not marked as pausing us")
	}
	if st := f.status(t, id).Status; st != store.OutboxHeld {
		t.Fatalf("item status = %s, want held", st)
	}
	if len(cut.reasons) != 1 || cut.reasons[0] != CutOffPausedByPeer {
		t.Fatalf("cut-offs = %v, want one %q", cut.reasons, CutOffPausedByPeer)
	}
}

// A pass stuck on a relay that does not answer must not hold up another
// pass past that pass's own deadline: the kill flush waits at most
// KillFlushTimeout.
func TestSendDueWaitsForThePassOnlyUntilItsDeadline(t *testing.T) {
	f := newOutboundFixture(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	f.mb.onSend = func(string) {
		entered <- struct{}{}
		<-release
	}
	f.send(t, "stuck")
	done := make(chan error, 1)
	go func() { done <- f.o.SendDue(context.Background()) }()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := f.o.SendDue(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second pass err = %v, want its deadline", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("second pass waited %v for the stuck one", waited)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
