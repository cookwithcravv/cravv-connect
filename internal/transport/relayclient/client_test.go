package relayclient_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/relayserver"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

const admin = "admin-secret"

type env struct {
	client *relayclient.Client
	clock  *core.FakeClock
}

func newEnv(t *testing.T, opts ...relayclient.Option) *env {
	t.Helper()
	clock := core.NewFakeClock(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))
	var h http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	srv, err := relayserver.New(relayserver.Config{PublicOrigin: ts.URL, AdminToken: admin, Clock: clock}, relayserver.NewMemoryBackend(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	h = srv
	c, err := relayclient.New(ts.URL, append([]relayclient.Option{relayclient.WithClock(clock)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return &env{client: c, clock: clock}
}

func ident(t *testing.T) *keys.Identity {
	t.Helper()
	id, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func (e *env) dial(t *testing.T, id *keys.Identity, creds transport.Credentials) transport.Mailbox {
	t.Helper()
	mb, err := e.client.Dialer().Dial(ctx(t), id, creds)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { mb.Close() })
	return mb
}

func recv(t *testing.T, mb transport.Mailbox) transport.Delivery {
	t.Helper()
	select {
	case d, ok := <-mb.Deliveries():
		if !ok {
			t.Fatalf("deliveries closed: %v", mb.Err())
		}
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	return transport.Delivery{}
}

func TestNewValidatesURL(t *testing.T) {
	good := map[string]string{
		"https://relay.example.com":      "https://relay.example.com",
		"http://127.0.0.1:8787/":         "http://127.0.0.1:8787",
		"https://Relay.Example.com:443":  "https://relay.example.com",
		"HTTPS://relay.example.com.":     "https://relay.example.com",
		"http://relay.example.com:80":    "http://relay.example.com",
		"http://[::1]:8787":              "http://[::1]:8787",
		"https://relay.example.com:8443": "https://relay.example.com:8443",
	}
	for in, origin := range good {
		c, err := relayclient.New(in)
		if err != nil || c.Origin() != origin {
			t.Errorf("New(%q) = %v, %v; want origin %q", in, c, err, origin)
		}
	}
	for _, bad := range []string{"ftp://x", "https://", "https://x/path", "https://x?q=1", "wss://x", "https://u:p@x"} {
		if _, err := relayclient.New(bad); err == nil {
			t.Errorf("New(%q) accepted", bad)
		}
	}
}

func TestRegisterInviteSendDeliverAck(t *testing.T) {
	e := newEnv(t)
	alice, bob := ident(t), ident(t)

	if _, err := e.client.Dialer().Dial(ctx(t), alice, transport.Credentials{}); !errors.Is(err, transport.ErrRelayForbidden) {
		t.Fatalf("no creds: err = %v, want ErrRelayForbidden", err)
	}
	a := e.dial(t, alice, transport.Credentials{AdminToken: admin})
	inv, err := a.RequestInvite(ctx(t))
	if err != nil || inv == "" {
		t.Fatalf("invite: %q %v", inv, err)
	}
	b := e.dial(t, bob, transport.Credentials{Invite: inv})

	if st, err := b.Send(ctx(t), alice.MachineID(), "m0", []byte("x")); err != nil || st != transport.SendNotAllowed {
		t.Fatalf("before allow: %s %v", st, err)
	}
	if err := a.Allow(ctx(t), bob.Public()); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"m1", "m2", "m3"} {
		st, err := b.Send(ctx(t), alice.MachineID(), id, []byte{byte(i)})
		if err != nil || st != transport.SendQueued {
			t.Fatalf("send %s: %s %v", id, st, err)
		}
	}
	for i, id := range []string{"m1", "m2", "m3"} {
		d := recv(t, a)
		if d.Seq != uint64(i+1) || d.ID != id || !d.From.Equal(bob.Public()) || !bytes.Equal(d.Frame, []byte{byte(i)}) {
			t.Fatalf("delivery %d = %+v", i, d)
		}
	}
	if err := a.Ack(ctx(t), 1); err != nil {
		t.Fatal(err)
	}
	// A request round trip guarantees the relay processed the ack.
	if _, err := a.RequestInvite(ctx(t)); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if !errors.Is(a.Err(), relayclient.ErrClosed) {
		t.Fatalf("Err after Close = %v", a.Err())
	}
	if _, ok := <-a.Deliveries(); ok {
		t.Fatal("deliveries not closed after Close")
	}

	a2 := e.dial(t, alice, transport.Credentials{})
	if d := recv(t, a2); d.Seq != 2 || d.ID != "m2" {
		t.Fatalf("redelivery = %+v", d)
	}
	if d := recv(t, a2); d.Seq != 3 {
		t.Fatalf("redelivery = %+v", d)
	}

	if err := a2.Deny(ctx(t), bob.Public()); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.Send(ctx(t), alice.MachineID(), "m4", []byte("x")); st != transport.SendNotAllowed {
		t.Fatalf("after deny: %s", st)
	}
	if st, _ := b.Send(ctx(t), ident(t).MachineID(), "m5", []byte("x")); st != transport.SendUnknownMailbox {
		t.Fatalf("unknown: %s", st)
	}
}

func TestSecondConnectionKicksFirst(t *testing.T) {
	e := newEnv(t)
	alice := ident(t)
	first := e.dial(t, alice, transport.Credentials{AdminToken: admin})
	second := e.dial(t, alice, transport.Credentials{})
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("first connection not closed")
	}
	var se *relayclient.ServerError
	if !errors.As(first.Err(), &se) || se.Code != "gone" {
		t.Fatalf("first.Err = %v, want gone", first.Err())
	}
	if _, err := second.RequestInvite(ctx(t)); err != nil {
		t.Fatalf("second connection unusable: %v", err)
	}
}

func TestRoomsEarlyMessageAndLowercaseNameplate(t *testing.T) {
	e := newEnv(t)
	a := e.dial(t, ident(t), transport.Credentials{AdminToken: admin})
	np, tok, err := a.CreateRoom(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	creator, err := e.client.Rooms().Open(ctx(t), np, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer creator.Close()
	if err := creator.Send(ctx(t), []byte("early")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	joiner, err := e.client.Rooms().Open(ctx(t), strings.ToLower(np), "")
	if err != nil {
		t.Fatal(err)
	}
	defer joiner.Close()
	if got, err := joiner.Recv(ctx(t)); err != nil || string(got) != "early" {
		t.Fatalf("joiner got %q %v", got, err)
	}
}

func TestRooms(t *testing.T) {
	e := newEnv(t)
	a := e.dial(t, ident(t), transport.Credentials{AdminToken: admin})
	np, tok, err := a.CreateRoom(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	creator, err := e.client.Rooms().Open(ctx(t), np, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer creator.Close()
	joiner, err := e.client.Rooms().Open(ctx(t), np, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := joiner.WaitPeer(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := creator.WaitPeer(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := creator.Send(ctx(t), []byte("pake-a")); err != nil {
		t.Fatal(err)
	}
	if got, err := joiner.Recv(ctx(t)); err != nil || string(got) != "pake-a" {
		t.Fatalf("joiner recv %q %v", got, err)
	}
	if err := joiner.Send(ctx(t), []byte("pake-b")); err != nil {
		t.Fatal(err)
	}
	if got, err := creator.Recv(ctx(t)); err != nil || string(got) != "pake-b" {
		t.Fatalf("creator recv %q %v", got, err)
	}
	if _, err := e.client.Rooms().Open(ctx(t), np, ""); !errors.Is(err, transport.ErrRoomGone) {
		t.Fatalf("second joiner err = %v", err)
	}
	joiner.Close()
	if _, err := creator.Recv(ctx(t)); !errors.Is(err, io.EOF) {
		t.Fatalf("after peer close err = %v, want EOF", err)
	}
	if _, err := e.client.Rooms().Open(ctx(t), np, tok); !errors.Is(err, transport.ErrRoomGone) {
		t.Fatalf("burned room err = %v", err)
	}
}

func TestBlobs(t *testing.T) {
	e := newEnv(t)
	up, rcpt, other := ident(t), ident(t), ident(t)
	a := e.dial(t, up, transport.Credentials{AdminToken: admin})
	for _, id := range []*keys.Identity{rcpt, other} {
		inv, err := a.RequestInvite(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		e.dial(t, id, transport.Credentials{Invite: inv})
	}
	ub, rb, ob := e.client.Blobs(up), e.client.Blobs(rcpt), e.client.Blobs(other)

	id, err := ub.Create(ctx(t), rcpt.Public(), core.FileChunkBytes+3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ub.PutChunk(ctx(t), id, 0, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := ub.PutChunk(ctx(t), id, 1, []byte("def")); err != nil {
		t.Fatal(err)
	}
	if got, err := rb.GetChunk(ctx(t), id, 1); err != nil || string(got) != "def" {
		t.Fatalf("recipient get %q %v", got, err)
	}
	if _, err := ob.GetChunk(ctx(t), id, 0); !errors.Is(err, transport.ErrRelayForbidden) {
		t.Fatalf("other get err = %v", err)
	}
	if _, err := ub.GetChunk(ctx(t), id, 0); !errors.Is(err, transport.ErrRelayForbidden) {
		t.Fatalf("uploader get err = %v", err)
	}
	if err := rb.PutChunk(ctx(t), id, 0, []byte("x")); !errors.Is(err, transport.ErrRelayForbidden) {
		t.Fatalf("recipient put err = %v", err)
	}
	if _, err := ub.Create(ctx(t), rcpt.Public(), core.MaxFileBytes+1, 101); !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("oversize err = %v", err)
	}
	if err := rb.Delete(ctx(t), id); err != nil {
		t.Fatal(err)
	}
	if _, err := rb.GetChunk(ctx(t), id, 0); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	var he *relayclient.HTTPError
	if _, err := e.client.Blobs(ident(t)).Create(ctx(t), rcpt.Public(), 1, 1); !errors.As(err, &he) || he.Status != http.StatusForbidden || he.Code != "forbidden" {
		t.Fatalf("non-member create err = %v", err)
	}
}
