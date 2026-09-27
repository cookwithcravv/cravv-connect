package relayserver

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

func TestHandshakeRejections(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	k := newKey(t)

	t.Run("missing routing ik", func(t *testing.T) {
		c := dialRaw(t, tr.wsURL(relayproto.PathConnect))
		c.expectError(relayproto.CodeBadRequest)
	})
	t.Run("unsupported version", func(t *testing.T) {
		c := dialRaw(t, tr.connectURL(k))
		c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{7}})
		c.expectError(relayproto.CodeUnsupportedVersion)
	})
	t.Run("first frame not hello", func(t *testing.T) {
		c := dialRaw(t, tr.connectURL(k))
		c.write(relayproto.Auth{T: relayproto.TypeAuth})
		c.expectError(relayproto.CodeBadRequest)
	})
	cases := []struct {
		name string
		sign func(nonce string) (ik string, sig string)
	}{
		{"bad signature", func(n string) (string, string) {
			return relayproto.B64(pubOf(k)), relayproto.B64(ed25519.Sign(k, []byte("wrong")))
		}},
		{"wrong origin", func(n string) (string, string) {
			return relayproto.B64(pubOf(k)), relayproto.B64(ed25519.Sign(k, relayproto.AuthMessage("https://evil.example", n)))
		}},
		{"ik differs from routing ik", func(n string) (string, string) {
			other := newKey(t)
			return relayproto.B64(pubOf(other)), relayproto.B64(ed25519.Sign(other, relayproto.AuthMessage(tr.ts.URL, n)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dialRaw(t, tr.connectURL(k))
			c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{1}})
			c.expect(relayproto.TypeWelcome)
			ch := c.expect(relayproto.TypeChallenge)
			ik, sig := tc.sign(ch["nonce"].(string))
			c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: ik, Sig: sig})
			c.expectError(relayproto.CodeAuthFailed)
		})
	}
}

func TestRegistration(t *testing.T) {
	tr := newTestRelay(t, Limits{})

	t.Run("unregistered op rejected", func(t *testing.T) {
		c, reg := tr.authed(t, newKey(t))
		if reg {
			t.Fatal("fresh key reported registered")
		}
		c.write(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "1"})
		c.expectError(relayproto.CodeNotRegistered)
	})
	t.Run("no credentials forbidden", func(t *testing.T) {
		c, _ := tr.authed(t, newKey(t))
		res := c.req(relayproto.Register{T: relayproto.TypeRegister, RID: "1"})
		if res["status"] != relayproto.StatusError || res["code"] != relayproto.CodeForbidden {
			t.Fatalf("res = %v", res)
		}
	})
	t.Run("wrong admin token forbidden", func(t *testing.T) {
		c, _ := tr.authed(t, newKey(t))
		res := c.req(relayproto.Register{T: relayproto.TypeRegister, RID: "1", AdminToken: "nope"})
		if res["code"] != relayproto.CodeForbidden {
			t.Fatalf("res = %v", res)
		}
	})
	t.Run("admin token then registered on reconnect", func(t *testing.T) {
		k := newKey(t)
		c := tr.member(t, k)
		c.ws.CloseNow()
		_, reg := tr.authed(t, k)
		if !reg {
			t.Fatal("not registered after admin registration")
		}
	})
	t.Run("invite single use and expiry", func(t *testing.T) {
		a := tr.member(t, newKey(t))
		inv := a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "i"})["invite"].(string)
		b, _ := tr.authed(t, newKey(t))
		if res := b.req(relayproto.Register{T: relayproto.TypeRegister, RID: "1", Invite: inv}); res["status"] != relayproto.StatusOK {
			t.Fatalf("first use: %v", res)
		}
		c, _ := tr.authed(t, newKey(t))
		if res := c.req(relayproto.Register{T: relayproto.TypeRegister, RID: "1", Invite: inv}); res["code"] != relayproto.CodeForbidden {
			t.Fatalf("second use: %v", res)
		}
		inv2 := a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "i2"})["invite"].(string)
		tr.clock.Advance(10*time.Minute + time.Second)
		d, _ := tr.authed(t, newKey(t))
		if res := d.req(relayproto.Register{T: relayproto.TypeRegister, RID: "1", Invite: inv2}); res["code"] != relayproto.CodeForbidden {
			t.Fatalf("expired invite: %v", res)
		}
	})
}

func TestLiveProtocolRules(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	k := newKey(t)
	tr.member(t, k).ws.CloseNow()

	t.Run("register again is ok", func(t *testing.T) {
		c, reg := tr.authed(t, k)
		if !reg {
			t.Fatal("not registered")
		}
		if res := c.req(relayproto.Register{T: relayproto.TypeRegister, RID: "again"}); res["status"] != relayproto.StatusOK {
			t.Fatalf("res = %v", res)
		}
	})
	t.Run("malformed ack closes", func(t *testing.T) {
		c, _ := tr.authed(t, k)
		c.write(map[string]any{"t": "ack", "seq": "not-a-number"})
		c.expectError(relayproto.CodeBadRequest)
	})
	t.Run("unknown type without rid closes", func(t *testing.T) {
		c, _ := tr.authed(t, k)
		c.write(map[string]any{"t": "frobnicate"})
		c.expectError(relayproto.CodeBadRequest)
	})
	t.Run("unknown type with rid keeps connection", func(t *testing.T) {
		c, _ := tr.authed(t, k)
		if res := c.req(map[string]any{"t": "frobnicate", "rid": "z"}); res["code"] != relayproto.CodeBadRequest {
			t.Fatalf("res = %v", res)
		}
		if res := c.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "after"}); res["status"] != relayproto.StatusOK {
			t.Fatalf("connection unusable after bad_request: %v", res)
		}
	})
}

func TestHealth(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	resp, err := http.Get(tr.ts.URL + relayproto.PathHealth)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h relayproto.Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil || !h.OK || h.Version != 1 {
		t.Fatalf("health = %+v %v", h, err)
	}
}

func TestPerIPConnectionLimit(t *testing.T) {
	tr := newTestRelay(t, Limits{IPRate: 1, IPBurst: 1})
	k := newKey(t)
	c := dialRaw(t, tr.connectURL(k))
	c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{1}})
	c.expect(relayproto.TypeWelcome)
	d := dialRaw(t, tr.connectURL(k))
	d.expectError(relayproto.CodeRateLimited)
}

func TestSendStatuses(t *testing.T) {
	tr := newTestRelay(t, Limits{QueueFrames: 2})
	ka, kb := newKey(t), newKey(t)
	a, b := tr.member(t, ka), tr.member(t, kb)

	if st := b.send(ka, "m1", []byte("x")); st != relayproto.StatusNotAllowed {
		t.Fatalf("before allow: %s", st)
	}
	a.allow(kb)
	if st := b.send(newKey(t), "m0", []byte("x")); st != relayproto.StatusUnknownMailbox {
		t.Fatalf("unknown: %s", st)
	}
	if st := b.send(ka, "big", make([]byte, 256<<10+1)); st != relayproto.StatusTooLarge {
		t.Fatalf("too large: %s", st)
	}
	if st := b.send(ka, "max", make([]byte, 256<<10)); st != relayproto.StatusQueued {
		t.Fatalf("exact max: %s", st)
	}
	if st := b.send(ka, "m2", []byte("x")); st != relayproto.StatusQueued {
		t.Fatalf("second: %s", st)
	}
	if st := b.send(ka, "m3", []byte("x")); st != relayproto.StatusQueueFull {
		t.Fatalf("third: %s", st)
	}
	res := a.req(relayproto.Deny{T: relayproto.TypeDeny, RID: "d", IK: relayproto.B64(pubOf(kb))})
	if res["status"] != relayproto.StatusOK {
		t.Fatalf("deny: %v", res)
	}
	if st := b.send(ka, "m4", []byte("x")); st != relayproto.StatusNotAllowed {
		t.Fatalf("after deny: %s", st)
	}
}

func TestQueueByteCap(t *testing.T) {
	tr := newTestRelay(t, Limits{QueueBytes: 10})
	ka, kb := newKey(t), newKey(t)
	a, b := tr.member(t, ka), tr.member(t, kb)
	a.allow(kb)
	if st := b.send(ka, "1", make([]byte, 6)); st != relayproto.StatusQueued {
		t.Fatal(st)
	}
	if st := b.send(ka, "2", make([]byte, 5)); st != relayproto.StatusQueueFull {
		t.Fatal(st)
	}
	if st := b.send(ka, "3", make([]byte, 4)); st != relayproto.StatusQueued {
		t.Fatal(st)
	}
}

// deliveries collects n deliver frames.
func deliveries(t *testing.T, c *wsc, n int) []map[string]any {
	t.Helper()
	var out []map[string]any
	for len(out) < n {
		tt, m := c.read()
		if tt == relayproto.TypeDeliver {
			out = append(out, m)
		}
	}
	return out
}

func TestDeliverAckRedeliver(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	ka, kb := newKey(t), newKey(t)
	a, b := tr.member(t, ka), tr.member(t, kb)
	a.allow(kb)
	for i := 1; i <= 3; i++ {
		if st := b.send(ka, fmt.Sprint("id", i), []byte{byte(i)}); st != relayproto.StatusQueued {
			t.Fatal(st)
		}
	}
	got := deliveries(t, a, 3)
	for i, d := range got {
		if d["seq"].(float64) != float64(i+1) || d["id"] != fmt.Sprint("id", i+1) || d["from"] != relayproto.B64(pubOf(kb)) {
			t.Fatalf("deliver %d = %v", i, d)
		}
		fr, _ := relayproto.UnB64(d["frame"].(string))
		if !bytes.Equal(fr, []byte{byte(i + 1)}) {
			t.Fatalf("frame %d = %v", i, fr)
		}
	}
	a.write(relayproto.Ack{T: relayproto.TypeAck, Seq: 1})
	// Round trip so the ack is processed before disconnecting.
	a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "sync"})
	a.ws.CloseNow()

	a2 := tr.member(t, ka)
	re := deliveries(t, a2, 2)
	if re[0]["seq"].(float64) != 2 || re[1]["seq"].(float64) != 3 {
		t.Fatalf("redelivered = %v", re)
	}
	a2.write(relayproto.Ack{T: relayproto.TypeAck, Seq: 3})
	a2.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "sync"})
	a2.ws.CloseNow()

	a3 := tr.member(t, ka)
	if m, err := a3.tryRead(200 * time.Millisecond); err == nil {
		t.Fatalf("acked frame redelivered: %v", m)
	}
	a3.ws.CloseNow()
	a4 := tr.member(t, ka)
	if st := b.send(ka, "id4", []byte{4}); st != relayproto.StatusQueued {
		t.Fatal(st)
	}
	if d := deliveries(t, a4, 1)[0]; d["seq"].(float64) != 4 {
		t.Fatalf("seq reused: %v", d)
	}
}

func TestQueueTTL(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	ka, kb := newKey(t), newKey(t)
	a, b := tr.member(t, ka), tr.member(t, kb)
	a.allow(kb)
	a.ws.CloseNow()
	if st := b.send(ka, "old", []byte("x")); st != relayproto.StatusQueued {
		t.Fatal(st)
	}
	tr.clock.Advance(7 * 24 * time.Hour)
	if st := b.send(ka, "new", []byte("y")); st != relayproto.StatusQueued {
		t.Fatal(st)
	}
	a2 := tr.member(t, ka)
	d := deliveries(t, a2, 1)[0]
	if d["id"] != "new" || d["seq"].(float64) != 2 {
		t.Fatalf("got %v, want only the unexpired frame", d)
	}
}

func TestSingleLiveConnection(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	ka, kb := newKey(t), newKey(t)
	first := tr.member(t, ka)
	first.allow(kb)
	second := tr.member(t, ka)
	first.expectError(relayproto.CodeGone)
	b := tr.member(t, kb)
	if st := b.send(ka, "x", []byte("x")); st != relayproto.StatusQueued {
		t.Fatal(st)
	}
	if d := deliveries(t, second, 1)[0]; d["id"] != "x" {
		t.Fatalf("got %v", d)
	}
}

func TestRateLimit(t *testing.T) {
	tr := newTestRelay(t, Limits{OpRate: 1, OpBurst: 2})
	ka := newKey(t)
	a := tr.member(t, ka)
	ask := func() string {
		return a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "r"})["status"].(string)
	}
	if ask() != relayproto.StatusOK || ask() != relayproto.StatusOK {
		t.Fatal("burst not honored")
	}
	if st := ask(); st != relayproto.StatusRateLimited {
		t.Fatalf("third = %s", st)
	}
	tr.clock.Advance(time.Second)
	if st := ask(); st != relayproto.StatusOK {
		t.Fatalf("after refill = %s", st)
	}
}

func TestPaddedBase64Accepted(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	k := newKey(t)
	padded := url.QueryEscape(relayproto.B64(pubOf(k)) + "=")
	c := dialRaw(t, tr.wsURL(relayproto.PathConnect+"?ik="+padded))
	c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{1}})
	c.expect(relayproto.TypeWelcome)
	ch := c.expect(relayproto.TypeChallenge)
	sig := ed25519.Sign(k, relayproto.AuthMessage(tr.ts.URL, ch["nonce"].(string)))
	c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: relayproto.B64(pubOf(k)) + "=", Sig: relayproto.B64(sig) + "=="})
	c.expect(relayproto.TypeAuthOK)
}

// The connection whose handshake the client saw finish last is the live one,
// even if the server is slow to finish setting up an earlier connection: a
// connection becomes live before its final handshake reply, so a late
// earlier connection can never kick a newer one.
func TestNewestHandshakeWinsEvenIfEarlierSetupIsSlow(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	ka, kb := newKey(t), newKey(t)
	release := make(chan struct{})
	var once sync.Once
	afterHandshake = func() {
		first := false
		once.Do(func() { first = true })
		if first {
			<-release // hold the first connection between handshake and setup
		}
	}
	t.Cleanup(func() { afterHandshake = nil })

	first := tr.member(t, ka)  // client sees the handshake finish
	second := tr.member(t, ka) // then a newer connection finishes too
	close(release)             // the first one's setup completes late
	first.expectError(relayproto.CodeGone)

	b := tr.member(t, kb)
	second.allow(kb)
	if st := b.send(ka, "x", []byte("x")); st != relayproto.StatusQueued {
		t.Fatal(st)
	}
	if d := deliveries(t, second, 1)[0]; d["id"] != "x" {
		t.Fatalf("got %v", d)
	}
}
