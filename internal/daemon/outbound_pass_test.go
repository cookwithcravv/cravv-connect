package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
