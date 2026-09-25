package relayclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
	"github.com/cravv/cravv-connect/internal/transport/relayclient"
)

// The reader must never stall on an undrained Deliveries channel: request replies
// queued behind hundreds of deliver frames still have to arrive.
func TestUndrainedDeliveriesDoNotBlockReplies(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := ident(t), ident(t), ident(t)
	a := e.dial(t, alice, transport.Credentials{AdminToken: admin})
	b := e.dial(t, bob, transport.Credentials{AdminToken: admin})
	e.dial(t, carol, transport.Credentials{AdminToken: admin})
	if err := a.Allow(ctx(t), bob.Public()); err != nil {
		t.Fatal(err)
	}
	a.Close()
	const n = 300
	for i := range n {
		if st, err := b.Send(ctx(t), alice.MachineID(), fmt.Sprintf("m%d", i), []byte{byte(i)}); err != nil || st != transport.SendQueued {
			t.Fatalf("send %d: %s %v", i, st, err)
		}
	}
	a2 := e.dial(t, alice, transport.Credentials{})
	// Give the relay time to push the whole backlog before the request goes out.
	time.Sleep(200 * time.Millisecond)
	actx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a2.Allow(actx, carol.Public()); err != nil {
		t.Fatalf("Allow with %d undrained deliveries: %v", n, err)
	}
	for i := range n {
		if d := recv(t, a2); d.ID != fmt.Sprintf("m%d", i) {
			t.Fatalf("delivery %d = %s", i, d.ID)
		}
	}
}

func TestKeepalivePingsKeepHealthyConnectionAlive(t *testing.T) {
	e := newEnv(t, relayclient.WithKeepalive(20*time.Millisecond, time.Second))
	a := e.dial(t, ident(t), transport.Credentials{AdminToken: admin})
	select {
	case <-a.Done():
		t.Fatalf("connection died: %v", a.Err())
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := a.RequestInvite(ctx(t)); err != nil {
		t.Fatal(err)
	}
}

// fakeSilentRelay completes the handshake and then never reads again, so pings go
// unanswered.
func fakeSilentRelay(t *testing.T) string {
	t.Helper()
	stop := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx := r.Context()
		write := func(v any) {
			b, _ := json.Marshal(v)
			_ = ws.Write(ctx, websocket.MessageText, b)
		}
		if _, _, err := ws.Read(ctx); err != nil { // hello
			return
		}
		write(relayproto.Welcome{T: relayproto.TypeWelcome, Version: relayproto.Version})
		write(relayproto.Challenge{T: relayproto.TypeChallenge, Nonce: relayproto.B64(make([]byte, 32))})
		if _, _, err := ws.Read(ctx); err != nil { // auth
			return
		}
		write(relayproto.AuthOK{T: relayproto.TypeAuthOK, Registered: true, MailboxID: "x"})
		<-stop
	}))
	t.Cleanup(func() { close(stop); ts.Close() })
	return ts.URL
}

func TestKeepalivePingFailureEndsConnection(t *testing.T) {
	c, err := relayclient.New(fakeSilentRelay(t), relayclient.WithKeepalive(20*time.Millisecond, 100*time.Millisecond))
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
	case <-time.After(3 * time.Second):
		t.Fatal("connection with unanswered pings still considered alive")
	}
	if mb.Err() == nil {
		t.Fatal("Err is nil after ping failure")
	}
	if _, ok := <-mb.Deliveries(); ok {
		t.Fatal("Deliveries not closed after ping failure")
	}
}
