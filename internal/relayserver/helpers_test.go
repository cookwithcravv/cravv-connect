package relayserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

const testAdmin = "admin-secret"

type testRelay struct {
	srv   *Server
	ts    *httptest.Server
	clock *core.FakeClock
}

// newTestRelay starts a relay whose PublicOrigin is the httptest URL.
func newTestRelay(t *testing.T, lim Limits) *testRelay {
	t.Helper()
	return newTestRelayCfg(t, func(c *Config) { c.Limits = lim })
}

// newTestRelayCfg is newTestRelay with a hook to adjust the Config before New.
// The hook sees PublicOrigin already set to the httptest URL.
func newTestRelayCfg(t *testing.T, mod func(*Config)) *testRelay {
	t.Helper()
	tr := &testRelay{clock: core.NewFakeClock(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))}
	tr.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tr.srv.ServeHTTP(w, r) }))
	t.Cleanup(tr.ts.Close)
	cfg := Config{PublicOrigin: tr.ts.URL, AdminToken: testAdmin, Clock: tr.clock}
	if mod != nil {
		mod(&cfg)
	}
	srv, err := New(cfg, NewMemoryBackend(tr.clock))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	tr.srv = srv
	return tr
}

func (tr *testRelay) wsURL(path string) string {
	return "ws" + strings.TrimPrefix(tr.ts.URL, "http") + path
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func pubOf(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

// wsc is a minimal raw relay-v1 test client.
type wsc struct {
	t  *testing.T
	ws *websocket.Conn
}

func dialRaw(t *testing.T, u string) *wsc {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", u, err)
	}
	ws.SetReadLimit(4 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	return &wsc{t: t, ws: ws}
}

func (c *wsc) write(v any) {
	c.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// read returns the next frame as a generic map and its "t".
func (c *wsc) read() (string, map[string]any) {
	c.t.Helper()
	m, err := c.tryRead(5 * time.Second)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return m["t"].(string), m
}

func (c *wsc) tryRead(d time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, b, err := c.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *wsc) expect(tt string) map[string]any {
	c.t.Helper()
	got, m := c.read()
	if got != tt {
		c.t.Fatalf("got frame %v, want t=%q", m, tt)
	}
	return m
}

func (c *wsc) expectError(code string) {
	c.t.Helper()
	m := c.expect(relayproto.TypeError)
	if m["code"] != code {
		c.t.Fatalf("error code = %v, want %q", m["code"], code)
	}
}

// connectURL is /v1/connect with the routing ik for key.
func (tr *testRelay) connectURL(k ed25519.PrivateKey) string {
	return tr.wsURL(relayproto.PathConnect + "?" + relayproto.QueryIK + "=" + url.QueryEscape(relayproto.B64(pubOf(k))))
}

// authed dials, runs hello..auth_ok and returns the client plus auth_ok.registered.
func (tr *testRelay) authed(t *testing.T, k ed25519.PrivateKey) (*wsc, bool) {
	t.Helper()
	c := dialRaw(t, tr.connectURL(k))
	c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{1}})
	c.expect(relayproto.TypeWelcome)
	ch := c.expect(relayproto.TypeChallenge)
	sig := ed25519.Sign(k, relayproto.AuthMessage(tr.ts.URL, ch["nonce"].(string)))
	c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: relayproto.B64(pubOf(k)), Sig: relayproto.B64(sig)})
	ok := c.expect(relayproto.TypeAuthOK)
	if ok["mailbox_id"] != relayproto.MailboxID(pubOf(k)) {
		t.Fatalf("mailbox_id = %v", ok["mailbox_id"])
	}
	return c, ok["registered"].(bool)
}

// member returns a live connection for a registered mailbox (registering with the admin token if needed).
func (tr *testRelay) member(t *testing.T, k ed25519.PrivateKey) *wsc {
	t.Helper()
	c, reg := tr.authed(t, k)
	if !reg {
		c.write(relayproto.Register{T: relayproto.TypeRegister, RID: "r", AdminToken: testAdmin})
		res := c.expect(relayproto.TypeRes)
		if res["status"] != relayproto.StatusOK {
			t.Fatalf("register: %v", res)
		}
	}
	return c
}

// req sends a request frame and returns the matching res.
func (c *wsc) req(v any) map[string]any {
	c.t.Helper()
	c.write(v)
	for {
		tt, m := c.read()
		if tt == relayproto.TypeRes {
			return m
		}
	}
}

func (c *wsc) allow(k ed25519.PrivateKey) {
	c.t.Helper()
	res := c.req(relayproto.Allow{T: relayproto.TypeAllow, RID: "allow", IK: relayproto.B64(pubOf(k))})
	if res["status"] != relayproto.StatusOK {
		c.t.Fatalf("allow: %v", res)
	}
}

func (c *wsc) send(to ed25519.PrivateKey, id string, frame []byte) string {
	c.t.Helper()
	res := c.req(relayproto.Send{T: relayproto.TypeSend, RID: "s-" + id, To: relayproto.MailboxID(pubOf(to)), ID: id, Frame: relayproto.B64(frame)})
	return res["status"].(string)
}
