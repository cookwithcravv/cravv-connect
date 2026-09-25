package conformance

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

// rawConn is a bare relay-v1 WebSocket for inputs relayclient never produces.
type rawConn struct {
	t  *testing.T
	ws *websocket.Conn
}

func (s *suite) wsURL(path string) string {
	return strings.Replace(strings.Replace(s.client.Origin(), "https://", "wss://", 1), "http://", "ws://", 1) + path
}

// rawConnect dials /v1/connect with the given raw query (e.g. "ik=...").
func (s *suite) rawConnect(t *testing.T, query string) *rawConn {
	t.Helper()
	u := s.wsURL(relayproto.PathConnect)
	if query != "" {
		u += "?" + query
	}
	ws, _, err := websocket.Dial(ctxT(t), u, nil)
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	ws.SetReadLimit(1 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	return &rawConn{t: t, ws: ws}
}

func ikQuery(id transport.Signer) string {
	return relayproto.QueryIK + "=" + url.QueryEscape(relayproto.B64(id.Public()))
}

func (c *rawConn) write(v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.ws.Write(ctxT(c.t), websocket.MessageText, b); err != nil {
		c.t.Fatalf("raw write: %v", err)
	}
}

func (c *rawConn) read() map[string]any {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, b, err := c.ws.Read(ctx)
	if err != nil {
		c.t.Fatalf("raw read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		c.t.Fatalf("raw frame %q: %v", b, err)
	}
	return m
}

func (c *rawConn) expect(tt string) map[string]any {
	c.t.Helper()
	m := c.read()
	if m["t"] != tt {
		c.t.Fatalf("got %v, want t=%q", m, tt)
	}
	return m
}

func (c *rawConn) expectError(code string) {
	c.t.Helper()
	m := c.expect(relayproto.TypeError)
	if m["code"] != code {
		c.t.Fatalf("error code %v, want %q", m["code"], code)
	}
}

// hello sends hello and returns the challenge nonce.
func (c *rawConn) hello() string {
	c.t.Helper()
	c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{relayproto.Version}})
	c.expect(relayproto.TypeWelcome)
	return c.expect(relayproto.TypeChallenge)["nonce"].(string)
}

// auth answers the challenge as id and returns auth_ok.
func (c *rawConn) auth(origin, nonce string, id transport.Signer) map[string]any {
	c.t.Helper()
	sig := id.Sign(relayproto.AuthMessage(origin, nonce))
	c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: relayproto.B64(id.Public()), Sig: relayproto.B64(sig)})
	return c.expect(relayproto.TypeAuthOK)
}
