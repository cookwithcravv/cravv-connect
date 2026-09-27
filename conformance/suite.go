// Package conformance is the black-box relay-v1 test suite. Any relay that passes
// Run is a valid cravv-connect relay. The suite talks to the relay only over the
// wire: through relayclient, plus a raw WebSocket helper for malformed input that
// the real client never sends.
package conformance

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// Target is the relay under test.
type Target struct {
	URL         string // scheme://host[:port], as clients dial it
	AdminToken  string
	NewIdentity func() transport.Signer // fresh identity per call
	// Clock is the relay's clock, used for request-signature timestamps. Nil means system time.
	Clock core.Clock
	// Advance moves the relay's clock forward. Nil (any external relay) skips the TTL cases.
	Advance func(time.Duration)
}

// SlowEnv enables the cases that fill a mailbox queue to its caps.
const SlowEnv = "CRAVV_CONFORMANCE_SLOW"

type testCase struct {
	name string
	run  func(t *testing.T, s *suite)
}

type suite struct {
	tg     Target
	client *relayclient.Client
}

// Run executes every conformance case as a named subtest of t.
func Run(t *testing.T, tg Target) {
	var opts []relayclient.Option
	if tg.Clock != nil {
		opts = append(opts, relayclient.WithClock(tg.Clock))
	}
	c, err := relayclient.New(tg.URL, opts...)
	if err != nil {
		t.Fatalf("relay url: %v", err)
	}
	s := &suite{tg: tg, client: c}
	groups := [][]testCase{healthCases(), handshakeCases(), registerCases(), mailboxCases(), queueCapCases(), roomCases(), blobCases(), ttlCases()}
	for _, g := range groups {
		for _, tc := range g {
			t.Run(tc.name, func(t *testing.T) { tc.run(t, s) })
		}
	}
}

func slow(t *testing.T) {
	t.Helper()
	if os.Getenv(SlowEnv) != "1" {
		t.Skipf("set %s=1 to run queue-cap cases", SlowEnv)
	}
}

// clockControl skips the test unless the target's clock can be advanced.
func (s *suite) clockControl(t *testing.T) func(time.Duration) {
	t.Helper()
	if s.tg.Advance == nil {
		t.Skip("TTL cases need a relay clock the suite can advance (in-process relay only)")
	}
	return s.tg.Advance
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// dial opens a mailbox and closes it when the test ends.
func (s *suite) dial(t *testing.T, id transport.Signer, creds transport.Credentials) transport.Mailbox {
	t.Helper()
	mb, err := s.client.Dialer().Dial(ctxT(t), id, creds)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { mb.Close() })
	return mb
}

// member returns a fresh identity registered with the admin token, and its mailbox.
func (s *suite) member(t *testing.T) (transport.Signer, transport.Mailbox) {
	t.Helper()
	id := s.tg.NewIdentity()
	return id, s.dial(t, id, transport.Credentials{AdminToken: s.tg.AdminToken})
}

// pair returns two members where a allows b to send to it.
func (s *suite) pair(t *testing.T) (a transport.Signer, amb transport.Mailbox, b transport.Signer, bmb transport.Mailbox) {
	t.Helper()
	a, amb = s.member(t)
	b, bmb = s.member(t)
	if err := amb.Allow(ctxT(t), b.Public()); err != nil {
		t.Fatalf("allow: %v", err)
	}
	return a, amb, b, bmb
}

func recv(t *testing.T, mb transport.Mailbox) transport.Delivery {
	t.Helper()
	select {
	case d, ok := <-mb.Deliveries():
		if !ok {
			t.Fatalf("connection ended: %v", mb.Err())
		}
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for deliver")
	}
	return transport.Delivery{}
}

func expectNoDelivery(t *testing.T, mb transport.Mailbox, wait time.Duration) {
	t.Helper()
	select {
	case d, ok := <-mb.Deliveries():
		if ok {
			t.Fatalf("unexpected deliver seq=%d id=%s", d.Seq, d.ID)
		}
	case <-time.After(wait):
	}
}

// sync makes a request round trip so every earlier frame (such as ack) was processed.
func sync(t *testing.T, mb transport.Mailbox) {
	t.Helper()
	if _, err := mb.RequestInvite(ctxT(t)); err != nil {
		t.Fatalf("sync round trip: %v", err)
	}
}

// send sends and retries while the relay answers rate_limited.
func send(t *testing.T, mb transport.Mailbox, to transport.Signer, id string, frame []byte) transport.SendStatus {
	t.Helper()
	for {
		st, err := mb.Send(ctxT(t), mailboxOf(to), id, frame)
		if err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
		if st != transport.SendRateLimited {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func serverCode(err error) string {
	var se *relayclient.ServerError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func httpStatus(err error) int {
	var he *relayclient.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// badSigner has a valid public key but signs with garbage.
type badSigner struct{ transport.Signer }

func (b badSigner) Sign(msg []byte) []byte {
	sig := b.Signer.Sign(msg)
	sig[0] ^= 0xff
	return sig
}
