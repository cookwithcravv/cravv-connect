package conformance

import (
	"net/url"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

func handshakeCases() []testCase {
	return []testCase{
		{"handshake/welcome_challenge_auth_ok", func(t *testing.T, s *suite) {
			id := s.tg.NewIdentity()
			c := s.rawConnect(t, ikQuery(id))
			nonce := c.hello()
			if b, err := relayproto.UnB64(nonce); err != nil || len(b) < 16 {
				t.Fatalf("nonce %q must be base64 of at least 16 bytes", nonce)
			}
			ok := c.auth(s.client.Origin(), nonce, id)
			if ok["registered"] != false {
				t.Fatalf("fresh key registered = %v", ok["registered"])
			}
			if ok["mailbox_id"] != relayproto.MailboxID(id.Public()) {
				t.Fatalf("mailbox_id = %v, want %s", ok["mailbox_id"], relayproto.MailboxID(id.Public()))
			}
		}},
		{"handshake/nonce_fresh_per_connection", func(t *testing.T, s *suite) {
			id := s.tg.NewIdentity()
			n1 := s.rawConnect(t, ikQuery(id)).hello()
			n2 := s.rawConnect(t, ikQuery(id)).hello()
			if n1 == n2 {
				t.Fatal("relay reused a challenge nonce")
			}
		}},
		{"handshake/unsupported_version", func(t *testing.T, s *suite) {
			c := s.rawConnect(t, ikQuery(s.tg.NewIdentity()))
			c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{99}})
			c.expectError(relayproto.CodeUnsupportedVersion)
		}},
		{"handshake/first_frame_must_be_hello", func(t *testing.T, s *suite) {
			c := s.rawConnect(t, ikQuery(s.tg.NewIdentity()))
			c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: "x", Sig: "y"})
			c.expectError(relayproto.CodeBadRequest)
		}},
		{"handshake/missing_routing_ik", func(t *testing.T, s *suite) {
			c := s.rawConnect(t, "")
			c.write(relayproto.Hello{T: relayproto.TypeHello, Versions: []int{relayproto.Version}})
			c.expectError(relayproto.CodeBadRequest)
		}},
		{"handshake/auth_ik_must_match_routing_ik", func(t *testing.T, s *suite) {
			routed, actual := s.tg.NewIdentity(), s.tg.NewIdentity()
			c := s.rawConnect(t, ikQuery(routed))
			nonce := c.hello()
			sig := actual.Sign(relayproto.AuthMessage(s.client.Origin(), nonce))
			c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: relayproto.B64(actual.Public()), Sig: relayproto.B64(sig)})
			c.expectError(relayproto.CodeAuthFailed)
		}},
		{"handshake/bad_signature", func(t *testing.T, s *suite) {
			id, _ := s.member(t)
			_, err := s.client.Dialer().Dial(ctxT(t), badSigner{id}, transport.Credentials{})
			if serverCode(err) != relayproto.CodeAuthFailed {
				t.Fatalf("err = %v, want auth_failed", err)
			}
		}},
		{"handshake/signature_bound_to_origin", func(t *testing.T, s *suite) {
			id := s.tg.NewIdentity()
			c := s.rawConnect(t, ikQuery(id))
			nonce := c.hello()
			sig := id.Sign(relayproto.AuthMessage("https://other-relay.invalid", nonce))
			c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: relayproto.B64(id.Public()), Sig: relayproto.B64(sig)})
			c.expectError(relayproto.CodeAuthFailed)
		}},
		{"handshake/padded_base64_accepted", func(t *testing.T, s *suite) {
			id := s.tg.NewIdentity()
			c := s.rawConnect(t, relayproto.QueryIK+"="+url.QueryEscape(relayproto.B64(id.Public())+"="))
			nonce := c.hello()
			sig := id.Sign(relayproto.AuthMessage(s.client.Origin(), nonce))
			c.write(relayproto.Auth{T: relayproto.TypeAuth, IK: relayproto.B64(id.Public()) + "=", Sig: relayproto.B64(sig) + "=="})
			c.expect(relayproto.TypeAuthOK)
		}},
	}
}
