package conformance

import (
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

func registerCases() []testCase {
	return []testCase{
		{"register/unregistered_op_rejected", func(t *testing.T, s *suite) {
			id := s.tg.NewIdentity()
			c := s.rawConnect(t, ikQuery(id))
			c.auth(s.client.Origin(), c.hello(), id)
			c.write(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "1"})
			c.expectError(relayproto.CodeNotRegistered)
		}},
		{"register/forbidden_without_credentials", func(t *testing.T, s *suite) {
			_, err := s.client.Dialer().Dial(ctxT(t), s.tg.NewIdentity(), transport.Credentials{})
			if !errors.Is(err, transport.ErrRelayForbidden) {
				t.Fatalf("err = %v, want forbidden", err)
			}
		}},
		{"register/forbidden_with_wrong_admin_token", func(t *testing.T, s *suite) {
			_, err := s.client.Dialer().Dial(ctxT(t), s.tg.NewIdentity(), transport.Credentials{AdminToken: s.tg.AdminToken + "x"})
			if !errors.Is(err, transport.ErrRelayForbidden) {
				t.Fatalf("err = %v, want forbidden", err)
			}
		}},
		{"register/forbidden_with_unknown_invite", func(t *testing.T, s *suite) {
			_, err := s.client.Dialer().Dial(ctxT(t), s.tg.NewIdentity(), transport.Credentials{Invite: "not-an-invite"})
			if !errors.Is(err, transport.ErrRelayForbidden) {
				t.Fatalf("err = %v, want forbidden", err)
			}
		}},
		{"register/admin_token_persists", func(t *testing.T, s *suite) {
			id, mb := s.member(t)
			mb.Close()
			c := s.rawConnect(t, ikQuery(id))
			if ok := c.auth(s.client.Origin(), c.hello(), id); ok["registered"] != true {
				t.Fatalf("registered = %v after admin registration", ok["registered"])
			}
		}},
		{"register/register_when_registered_is_ok", func(t *testing.T, s *suite) {
			id, mb := s.member(t)
			mb.Close()
			c := s.rawConnect(t, ikQuery(id))
			c.auth(s.client.Origin(), c.hello(), id)
			c.write(relayproto.Register{T: relayproto.TypeRegister, RID: "again"})
			if m := c.expect(relayproto.TypeRes); m["rid"] != "again" || m["status"] != relayproto.StatusOK {
				t.Fatalf("res = %v", m)
			}
		}},
		{"register/refused_gets_res_then_close", func(t *testing.T, s *suite) {
			id := s.tg.NewIdentity()
			c := s.rawConnect(t, ikQuery(id))
			c.auth(s.client.Origin(), c.hello(), id)
			c.write(relayproto.Register{T: relayproto.TypeRegister, RID: "reg", Invite: "not-an-invite"})
			m := c.expect(relayproto.TypeRes)
			if m["rid"] != "reg" || m["status"] != relayproto.StatusError || m["code"] != relayproto.CodeForbidden {
				t.Fatalf("res = %v, want error forbidden", m)
			}
			c.expectClosed()
		}},
		{"register/outstanding_invites_capped", func(t *testing.T, s *suite) {
			_, a := s.member(t)
			for i := range 20 {
				if _, err := a.RequestInvite(ctxT(t)); err != nil {
					t.Fatalf("invite %d: %v", i+1, err)
				}
			}
			if _, err := a.RequestInvite(ctxT(t)); serverCode(err) != relayproto.CodeRateLimited {
				t.Fatalf("21st outstanding invite: %v, want error rate_limited", err)
			}
		}},
		{"register/invite_single_use", func(t *testing.T, s *suite) {
			_, a := s.member(t)
			inv, err := a.RequestInvite(ctxT(t))
			if err != nil || inv == "" {
				t.Fatalf("invite_request: %q %v", inv, err)
			}
			s.dial(t, s.tg.NewIdentity(), transport.Credentials{Invite: inv})
			_, err = s.client.Dialer().Dial(ctxT(t), s.tg.NewIdentity(), transport.Credentials{Invite: inv})
			if !errors.Is(err, transport.ErrRelayForbidden) {
				t.Fatalf("second use err = %v, want forbidden", err)
			}
		}},
		{"register/invites_unique", func(t *testing.T, s *suite) {
			_, a := s.member(t)
			i1, err1 := a.RequestInvite(ctxT(t))
			i2, err2 := a.RequestInvite(ctxT(t))
			if err1 != nil || err2 != nil || i1 == i2 {
				t.Fatalf("invites %q %q (%v %v)", i1, i2, err1, err2)
			}
		}},
	}
}
