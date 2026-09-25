package relayserver

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/relayproto"
)

func (tr *testRelay) createRoom(t *testing.T) (nameplate, token string) {
	t.Helper()
	c := tr.member(t, newKey(t))
	res := c.req(relayproto.RoomCreate{T: relayproto.TypeRoomCreate, RID: "room"})
	if res["status"] != relayproto.StatusOK {
		t.Fatalf("room_create: %v", res)
	}
	np, tok := res["nameplate"].(string), res["creator_token"].(string)
	if len(np) != 4 || len(tok) != 32 {
		t.Fatalf("nameplate %q token %q", np, tok)
	}
	return np, tok
}

func (tr *testRelay) pairURL(np, tok string) string {
	u := relayproto.PathPair + np
	if tok != "" {
		u += "?" + relayproto.QueryToken + "=" + url.QueryEscape(tok)
	}
	return tr.wsURL(u)
}

func TestRoomHappyPathAndBurn(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	np, tok := tr.createRoom(t)

	creator := dialRaw(t, tr.pairURL(np, tok))
	creator.expect(relayproto.TypeWaiting)
	joiner := dialRaw(t, tr.pairURL(np, ""))
	joiner.expect(relayproto.TypePeerJoined)
	creator.expect(relayproto.TypePeerJoined)

	creator.write(relayproto.RoomMsg{T: relayproto.TypeMsg, Data: relayproto.B64([]byte("from A"))})
	if m := joiner.expect(relayproto.TypeMsg); m["data"] != relayproto.B64([]byte("from A")) {
		t.Fatalf("joiner got %v", m)
	}
	joiner.write(relayproto.RoomMsg{T: relayproto.TypeMsg, Data: relayproto.B64([]byte("from B"))})
	if m := creator.expect(relayproto.TypeMsg); m["data"] != relayproto.B64([]byte("from B")) {
		t.Fatalf("creator got %v", m)
	}

	second := dialRaw(t, tr.pairURL(np, ""))
	second.expectError(relayproto.CodeGone)

	joiner.ws.CloseNow()
	creator.expect(relayproto.TypeClosed)

	again := dialRaw(t, tr.pairURL(np, tok))
	again.expectError(relayproto.CodeNotFound)
}

func TestRoomBuffersCreatorMessagesAndIgnoresCase(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	np, tok := tr.createRoom(t)
	creator := dialRaw(t, tr.pairURL(np, tok))
	creator.expect(relayproto.TypeWaiting)
	creator.write(relayproto.RoomMsg{T: relayproto.TypeMsg, Data: relayproto.B64([]byte("early-1"))})
	creator.write(relayproto.RoomMsg{T: relayproto.TypeMsg, Data: relayproto.B64([]byte("early-2"))})
	// Let the relay read both messages before the joiner arrives.
	time.Sleep(50 * time.Millisecond)
	joiner := dialRaw(t, tr.pairURL(strings.ToLower(np), ""))
	joiner.expect(relayproto.TypePeerJoined)
	for _, want := range []string{"early-1", "early-2"} {
		if m := joiner.expect(relayproto.TypeMsg); m["data"] != relayproto.B64([]byte(want)) {
			t.Fatalf("joiner got %v, want %s", m, want)
		}
	}
	creator.expect(relayproto.TypePeerJoined)
}

func TestRoomRejections(t *testing.T) {
	tr := newTestRelay(t, Limits{})

	t.Run("unknown nameplate", func(t *testing.T) {
		c := dialRaw(t, tr.pairURL("ZZZZ", ""))
		c.expectError(relayproto.CodeNotFound)
	})
	t.Run("wrong creator token", func(t *testing.T) {
		np, _ := tr.createRoom(t)
		c := dialRaw(t, tr.pairURL(np, "wrong"))
		c.expectError(relayproto.CodeForbidden)
	})
	t.Run("joiner before creator", func(t *testing.T) {
		np, tok := tr.createRoom(t)
		c := dialRaw(t, tr.pairURL(np, ""))
		c.expectError(relayproto.CodeNotFound)
		creator := dialRaw(t, tr.pairURL(np, tok))
		creator.expect(relayproto.TypeWaiting)
	})
	t.Run("second creator", func(t *testing.T) {
		np, tok := tr.createRoom(t)
		first := dialRaw(t, tr.pairURL(np, tok))
		first.expect(relayproto.TypeWaiting)
		second := dialRaw(t, tr.pairURL(np, tok))
		second.expectError(relayproto.CodeGone)
	})
	t.Run("expired", func(t *testing.T) {
		np, tok := tr.createRoom(t)
		tr.clock.Advance(10 * time.Minute)
		c := dialRaw(t, tr.pairURL(np, tok))
		c.expectError(relayproto.CodeGone)
	})
	t.Run("expires mid exchange", func(t *testing.T) {
		np, tok := tr.createRoom(t)
		creator := dialRaw(t, tr.pairURL(np, tok))
		creator.expect(relayproto.TypeWaiting)
		joiner := dialRaw(t, tr.pairURL(np, ""))
		joiner.expect(relayproto.TypePeerJoined)
		creator.expect(relayproto.TypePeerJoined)
		tr.clock.Advance(10 * time.Minute)
		creator.write(relayproto.RoomMsg{T: relayproto.TypeMsg, Data: relayproto.B64([]byte("late"))})
		creator.expectError(relayproto.CodeGone)
		joiner.expectError(relayproto.CodeGone)
	})
	t.Run("creator leaves before join burns room", func(t *testing.T) {
		np, tok := tr.createRoom(t)
		creator := dialRaw(t, tr.pairURL(np, tok))
		creator.expect(relayproto.TypeWaiting)
		creator.ws.CloseNow()
		deadline := time.Now().Add(2 * time.Second)
		for {
			c := dialRaw(t, tr.pairURL(np, ""))
			m, err := c.tryRead(2 * time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if m["t"] == relayproto.TypeError && m["code"] == relayproto.CodeNotFound {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("room still joinable: %v", m)
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}
