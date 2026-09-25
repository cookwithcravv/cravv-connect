package conformance

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

func mailboxCases() []testCase {
	return []testCase{
		{"mailbox/allow_list_enforced", func(t *testing.T, s *suite) {
			a, amb := s.member(t)
			b, bmb := s.member(t)
			if st := send(t, bmb, a, "m1", []byte("x")); st != transport.SendNotAllowed {
				t.Fatalf("before allow: %s", st)
			}
			if err := amb.Allow(ctxT(t), b.Public()); err != nil {
				t.Fatal(err)
			}
			if st := send(t, bmb, a, "m2", []byte("x")); st != transport.SendQueued {
				t.Fatalf("after allow: %s", st)
			}
			if err := amb.Deny(ctxT(t), b.Public()); err != nil {
				t.Fatal(err)
			}
			if st := send(t, bmb, a, "m3", []byte("x")); st != transport.SendNotAllowed {
				t.Fatalf("after deny: %s", st)
			}
			if d := recv(t, amb); d.ID != "m2" {
				t.Fatalf("delivered %q, want only m2", d.ID)
			}
			expectNoDelivery(t, amb, 300*time.Millisecond)
		}},
		{"mailbox/allow_is_directional", func(t *testing.T, s *suite) {
			a, amb, b, bmb := s.pair(t)
			if st := send(t, bmb, a, "b-to-a", []byte("x")); st != transport.SendQueued {
				t.Fatalf("b->a after a allowed b: %s", st)
			}
			if st := send(t, amb, b, "a-to-b", []byte("x")); st != transport.SendNotAllowed {
				t.Fatalf("a->b without b allowing a: %s", st)
			}
		}},
		{"mailbox/unknown_mailbox", func(t *testing.T, s *suite) {
			_, mb := s.member(t)
			if st := send(t, mb, s.tg.NewIdentity(), "m", []byte("x")); st != transport.SendUnknownMailbox {
				t.Fatalf("status %s, want unknown_mailbox", st)
			}
		}},
		{"mailbox/deliver_fields_and_seq_order", func(t *testing.T, s *suite) {
			a, amb, b, bmb := s.pair(t)
			for i := range 5 {
				id := fmt.Sprintf("m%d", i)
				if st := send(t, bmb, a, id, []byte(id)); st != transport.SendQueued {
					t.Fatalf("send %s: %s", id, st)
				}
			}
			var prev uint64
			for i := range 5 {
				d := recv(t, amb)
				id := fmt.Sprintf("m%d", i)
				if d.ID != id || !bytes.Equal(d.Frame, []byte(id)) || !d.From.Equal(b.Public()) {
					t.Fatalf("deliver %d = %+v", i, d)
				}
				if d.Seq <= prev {
					t.Fatalf("seq %d not greater than %d", d.Seq, prev)
				}
				prev = d.Seq
			}
		}},
		{"mailbox/redelivery_of_unacked", func(t *testing.T, s *suite) {
			a, amb, _, bmb := s.pair(t)
			for i := range 3 {
				send(t, bmb, a, fmt.Sprintf("r%d", i), []byte{byte(i)})
			}
			first := []transport.Delivery{recv(t, amb), recv(t, amb), recv(t, amb)}
			if err := amb.Ack(ctxT(t), first[0].Seq); err != nil {
				t.Fatal(err)
			}
			sync(t, amb)
			amb.Close()
			amb2 := s.dial(t, a, transport.Credentials{})
			for _, want := range first[1:] {
				d := recv(t, amb2)
				if d.Seq != want.Seq || d.ID != want.ID || !bytes.Equal(d.Frame, want.Frame) {
					t.Fatalf("redelivered %+v, want %+v", d, want)
				}
			}
			expectNoDelivery(t, amb2, 300*time.Millisecond)
		}},
		{"mailbox/ack_removes_and_seq_never_reused", func(t *testing.T, s *suite) {
			a, amb, _, bmb := s.pair(t)
			send(t, bmb, a, "x1", []byte("1"))
			send(t, bmb, a, "x2", []byte("2"))
			recv(t, amb)
			last := recv(t, amb)
			if err := amb.Ack(ctxT(t), last.Seq); err != nil {
				t.Fatal(err)
			}
			sync(t, amb)
			amb.Close()
			amb2 := s.dial(t, a, transport.Credentials{})
			expectNoDelivery(t, amb2, 300*time.Millisecond)
			send(t, bmb, a, "x3", []byte("3"))
			if d := recv(t, amb2); d.ID != "x3" || d.Seq <= last.Seq {
				t.Fatalf("after reconnect got seq %d id %s; want id x3 with seq > %d", d.Seq, d.ID, last.Seq)
			}
		}},
		{"mailbox/queued_while_offline", func(t *testing.T, s *suite) {
			a, amb, _, bmb := s.pair(t)
			amb.Close()
			send(t, bmb, a, "off", []byte("o"))
			amb2 := s.dial(t, a, transport.Credentials{})
			if d := recv(t, amb2); d.ID != "off" {
				t.Fatalf("got %s", d.ID)
			}
		}},
		{"mailbox/too_large", func(t *testing.T, s *suite) {
			a, _, _, bmb := s.pair(t)
			if st := send(t, bmb, a, "big", make([]byte, core.MaxFrameBytes+1)); st != transport.SendTooLarge {
				t.Fatalf("MaxFrameBytes+1: %s", st)
			}
			if st := send(t, bmb, a, "max", make([]byte, core.MaxFrameBytes)); st != transport.SendQueued {
				t.Fatalf("MaxFrameBytes: %s", st)
			}
		}},
		{"mailbox/single_live_connection", func(t *testing.T, s *suite) {
			a, first, _, bmb := s.pair(t)
			second := s.dial(t, a, transport.Credentials{})
			select {
			case <-first.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("older connection still open")
			}
			if code := serverCode(first.Err()); code != relayproto.CodeGone {
				t.Fatalf("older connection ended with %v, want error gone", first.Err())
			}
			send(t, bmb, a, "to-second", []byte("s"))
			if d := recv(t, second); d.ID != "to-second" {
				t.Fatalf("got %s", d.ID)
			}
		}},
		{"mailbox/unknown_request_type", func(t *testing.T, s *suite) {
			id, _ := s.member(t)
			c := s.rawConnect(t, ikQuery(id))
			c.auth(s.client.Origin(), c.hello(), id)
			c.write(map[string]string{"t": "no_such_op", "rid": "7"})
			m := c.expect(relayproto.TypeRes)
			if m["rid"] != "7" || m["status"] != relayproto.StatusError || m["code"] != relayproto.CodeBadRequest {
				t.Fatalf("res = %v", m)
			}
			c.write(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "8"})
			if m := c.expect(relayproto.TypeRes); m["rid"] != "8" || m["status"] != relayproto.StatusOK {
				t.Fatalf("connection not usable after bad_request: %v", m)
			}
		}},
		{"mailbox/unknown_type_without_rid_closes", func(t *testing.T, s *suite) {
			id, _ := s.member(t)
			c := s.rawConnect(t, ikQuery(id))
			c.auth(s.client.Origin(), c.hello(), id)
			c.write(map[string]string{"t": "no_such_op"})
			c.expectError(relayproto.CodeBadRequest)
		}},
		{"mailbox/malformed_ack_closes", func(t *testing.T, s *suite) {
			id, _ := s.member(t)
			c := s.rawConnect(t, ikQuery(id))
			c.auth(s.client.Origin(), c.hello(), id)
			c.write(map[string]any{"t": "ack", "seq": "seven"})
			c.expectError(relayproto.CodeBadRequest)
		}},
		{"mailbox/binary_message_closes", func(t *testing.T, s *suite) {
			_, c := s.rawMember(t)
			c.writeRaw(websocket.MessageBinary, []byte(`{"t":"invite_request","rid":"1"}`))
			c.expectError(relayproto.CodeBadRequest)
			c.expectClosed()
		}},
		{"mailbox/known_request_without_rid_closes", func(t *testing.T, s *suite) {
			_, c := s.rawMember(t)
			c.write(map[string]string{"t": relayproto.TypeAllow, "ik": relayproto.B64(s.tg.NewIdentity().Public())})
			c.expectError(relayproto.CodeBadRequest)
			c.expectClosed()
		}},
		{"mailbox/allow_bad_ik_bad_request", func(t *testing.T, s *suite) {
			_, c := s.rawMember(t)
			for i, ik := range []string{"not-base64!", relayproto.B64(make([]byte, 31)), ""} {
				rid := fmt.Sprint("a", i)
				c.write(relayproto.Allow{T: relayproto.TypeAllow, RID: rid, IK: ik})
				if m := c.expect(relayproto.TypeRes); m["rid"] != rid || m["status"] != relayproto.StatusError || m["code"] != relayproto.CodeBadRequest {
					t.Fatalf("allow ik %q: %v, want error bad_request", ik, m)
				}
			}
			c.write(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "after"})
			if m := c.expect(relayproto.TypeRes); m["rid"] != "after" || m["status"] != relayproto.StatusOK {
				t.Fatalf("connection not usable after bad allow: %v", m)
			}
		}},
		{"mailbox/no_duplicate_seq_within_connection", func(t *testing.T, s *suite) {
			a, amb, _, bmb := s.pair(t)
			seen := map[uint64]bool{}
			var prev uint64
			take := func(want string) {
				t.Helper()
				d := recv(t, amb)
				if d.ID != want {
					t.Fatalf("got %s, want %s", d.ID, want)
				}
				if seen[d.Seq] || d.Seq <= prev {
					t.Fatalf("seq %d pushed twice or out of order (prev %d)", d.Seq, prev)
				}
				seen[d.Seq], prev = true, d.Seq
			}
			for i := range 3 {
				send(t, bmb, a, fmt.Sprint("first", i), []byte{1})
			}
			for i := range 3 {
				take(fmt.Sprint("first", i))
			}
			// Unacked frames must not be pushed again on this connection when new ones arrive.
			for i := range 2 {
				send(t, bmb, a, fmt.Sprint("second", i), []byte{2})
			}
			for i := range 2 {
				take(fmt.Sprint("second", i))
			}
			sync(t, amb)
			expectNoDelivery(t, amb, 300*time.Millisecond)
		}},
		{"mailbox/deny_keeps_already_queued_frames", func(t *testing.T, s *suite) {
			a, amb, b, bmb := s.pair(t)
			amb.Close()
			send(t, bmb, a, "before-deny", []byte("x"))
			amb2 := s.dial(t, a, transport.Credentials{})
			if err := amb2.Deny(ctxT(t), b.Public()); err != nil {
				t.Fatal(err)
			}
			if d := recv(t, amb2); d.ID != "before-deny" {
				t.Fatalf("got %s, want the frame queued before deny", d.ID)
			}
		}},
		{"mailbox/res_echoes_rid", func(t *testing.T, s *suite) {
			id, _ := s.member(t)
			c := s.rawConnect(t, ikQuery(id))
			c.auth(s.client.Origin(), c.hello(), id)
			c.write(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "abc-123"})
			m := c.expect(relayproto.TypeRes)
			if m["rid"] != "abc-123" || m["status"] != relayproto.StatusOK || m["invite"] == "" {
				t.Fatalf("res = %v", m)
			}
		}},
	}
}
