package relayclient_test

import (
	"errors"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// Cloudflare's subrequest depth limit is reached by a long-lived connection:
// every call the relay then makes for it fails with internal until the client
// reconnects. So an internal answer on a connection old enough ends it, and
// the caller still gets the internal error to retry.
func TestInternalErrorRecyclesAnOldConnection(t *testing.T) {
	c, err := relayclient.New(fakeFlakyRelay(t), relayclient.WithInternalRecycle(0))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := c.Dialer().Dial(ctx(t), ident(t), transport.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	defer mb.Close()
	_, err = mb.Send(ctx(t), "peer", "m1", []byte("x"))
	if !errors.Is(err, transport.ErrRelayInternal) {
		t.Fatalf("send: %v, want ErrRelayInternal", err)
	}
	select {
	case <-mb.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("connection not ended after an internal answer")
	}
	if !errors.Is(mb.Err(), relayclient.ErrRecycled) {
		t.Fatalf("Err = %v, want ErrRecycled", mb.Err())
	}
}

// A connection is replaced after its maximum age, before it can get near
// the limit.
func TestConnectionEndsAtMaxAge(t *testing.T) {
	c, err := relayclient.New(fakeFlakyRelay(t), relayclient.WithMaxConnectionAge(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := c.Dialer().Dial(ctx(t), ident(t), transport.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	defer mb.Close()
	select {
	case <-mb.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("connection not ended at its maximum age")
	}
	if !errors.Is(mb.Err(), relayclient.ErrRecycled) {
		t.Fatalf("Err = %v, want ErrRecycled", mb.Err())
	}
}
