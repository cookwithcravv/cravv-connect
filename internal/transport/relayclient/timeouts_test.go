package relayclient_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// A request the relay never answers fails after the request timeout, even
// when the caller's context has no deadline, and the connection is ended so
// the daemon dials a fresh one.
func TestRequestTimeoutEndsTheConnection(t *testing.T) {
	c, err := relayclient.New(fakeSilentRelay(t), relayclient.WithKeepalive(0, 0),
		relayclient.WithRequestTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := c.Dialer().Dial(ctx(t), ident(t), transport.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	defer mb.Close()
	start := time.Now()
	_, err = mb.Send(context.Background(), ident(t).MachineID(), "m1", []byte("x"))
	if !errors.Is(err, relayclient.ErrRequestTimeout) {
		t.Fatalf("send err = %v, want ErrRequestTimeout", err)
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("send waited %v", waited)
	}
	select {
	case <-mb.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the connection was kept after a request timed out")
	}
	if !errors.Is(mb.Err(), relayclient.ErrRequestTimeout) {
		t.Fatalf("Err = %v", mb.Err())
	}
}
