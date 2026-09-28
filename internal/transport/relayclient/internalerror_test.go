package relayclient_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// fakeFlakyRelay completes the handshake and then answers the first send
// with res{status:"error",code:"internal"} and every later one with queued,
// the way a relay answers a per-request internal failure without closing
// the socket.
func fakeFlakyRelay(t *testing.T) string {
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
		sends := 0
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var req relayproto.Send
			if json.Unmarshal(raw, &req) != nil || req.T != relayproto.TypeSend {
				continue
			}
			sends++
			res := relayproto.Res{T: relayproto.TypeRes, RID: req.RID, Status: relayproto.StatusQueued}
			if sends == 1 {
				res.Status, res.Code = relayproto.StatusError, relayproto.CodeInternal
			}
			write(res)
			select {
			case <-stop:
				return
			default:
			}
		}
	}))
	t.Cleanup(func() { close(stop); ts.Close() })
	return ts.URL
}

// A per-request internal error fails that send with an error the daemon
// retries (transport.ErrRelayInternal) and leaves the connection up.
func TestSendInternalErrorKeepsConnection(t *testing.T) {
	c, err := relayclient.New(fakeFlakyRelay(t))
	if err != nil {
		t.Fatal(err)
	}
	mb, err := c.Dialer().Dial(ctx(t), ident(t), transport.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	defer mb.Close()
	to := ident(t).MachineID()
	_, err = mb.Send(ctx(t), to, "m1", []byte("x"))
	var se *relayclient.ServerError
	if !errors.As(err, &se) || se.Code != relayproto.CodeInternal || !errors.Is(err, transport.ErrRelayInternal) {
		t.Fatalf("send err = %v, want a ServerError internal matching transport.ErrRelayInternal", err)
	}
	select {
	case <-mb.Done():
		t.Fatalf("an internal error on one request ended the connection: %v", mb.Err())
	default:
	}
	if st, err := mb.Send(ctx(t), to, "m1", []byte("x")); err != nil || st != transport.SendQueued {
		t.Fatalf("retry on the same connection = %s, %v", st, err)
	}
}
