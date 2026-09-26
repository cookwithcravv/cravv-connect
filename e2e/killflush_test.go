package e2e

import (
	"context"
	"crypto/ed25519"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/relayserver"
)

// gatedBackend blocks Enqueue while gate is set, until release is closed.
type gatedBackend struct {
	relayserver.Backend
	gate    atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *gatedBackend) Enqueue(ctx context.Context, mailbox string, from ed25519.PublicKey, id string, frame []byte, lim relayserver.QueueLimits) (uint64, error) {
	if b.gate.Load() {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return b.Backend.Enqueue(ctx, mailbox, from, id, frame, lim)
}

// Spec 10: the kill switch takes effect for agents the moment kill starts,
// even while the flush of failed(killed) updates is still running.
func TestKillRefusesSendsDuringFlush(t *testing.T) {
	t.Parallel()
	gb := &gatedBackend{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gb.release) }) }
	r := NewRelayWith(t, func(b relayserver.Backend) relayserver.Backend { gb.Backend = b; return gb })
	t.Cleanup(release)
	_, a, b := NewPairOn(t, r)
	l := LinkUp(t, a, b, "tasks-auto")
	sa := l.A.C

	gb.gate.Store(true)
	sendChat(t, sa, l.ANum, "stuck in the relay")
	select {
	case <-gb.entered: // alice's send loop now waits on the relay: the kill flush will too
	case <-time.After(wait):
		t.Fatal("send never reached the relay")
	}
	killc := make(chan error, 1)
	go func() { killc <- TryCall(a.Conn(), ipc.MethodKill, nil, nil) }()
	Eventually(t, 2*time.Second, "status reports killed during the flush", func() bool { return a.Status().Killed })
	wantKind(t, TryCall(sa, ipc.MethodChatSend, ipc.ChatSendParams{Link: l.ANum, Text: "x"}, nil), ipc.KindKilled)
	wantKind(t, TryCall(sa, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: "x"}, nil), ipc.KindKilled)
	gb.gate.Store(false)
	release()
	if err := <-killc; err != nil {
		t.Fatalf("kill: %v", err)
	}
}
