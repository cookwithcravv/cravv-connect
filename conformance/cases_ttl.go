package conformance

import (
	"errors"
	"net/http"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/transport"
)

// ttlCases need Target.Advance; they are skipped against external relays.
func ttlCases() []testCase {
	return []testCase{
		{"ttl/invite_expires", func(t *testing.T, s *suite) {
			advance := s.clockControl(t)
			_, mb := s.member(t)
			inv, err := mb.RequestInvite(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			advance(core.InviteTTL)
			if _, err := s.client.Dialer().Dial(ctxT(t), s.tg.NewIdentity(), transport.Credentials{Invite: inv}); !errors.Is(err, transport.ErrRelayForbidden) {
				t.Fatalf("expired invite: %v, want forbidden", err)
			}
		}},
		{"ttl/queued_frames_expire", func(t *testing.T, s *suite) {
			advance := s.clockControl(t)
			a, amb, _, bmb := s.pair(t)
			amb.Close()
			send(t, bmb, a, "old", []byte("o"))
			advance(core.RelayTTL)
			send(t, bmb, a, "new", []byte("n"))
			amb2 := s.dial(t, a, transport.Credentials{})
			if d := recv(t, amb2); d.ID != "new" {
				t.Fatalf("got %s, want only the unexpired frame", d.ID)
			}
		}},
		{"ttl/room_expires", func(t *testing.T, s *suite) {
			advance := s.clockControl(t)
			_, mb := s.member(t)
			np, tok, err := mb.CreateRoom(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			creator, err := s.client.Rooms().Open(ctxT(t), np, tok)
			if err != nil {
				t.Fatal(err)
			}
			defer creator.Close()
			advance(core.RoomTTL)
			if _, err := s.client.Rooms().Open(ctxT(t), np, ""); !errors.Is(err, transport.ErrRoomGone) {
				t.Fatalf("expired room: %v, want room gone", err)
			}
		}},
		{"ttl/blob_expires_410", func(t *testing.T, s *suite) {
			advance := s.clockControl(t)
			up, _ := s.member(t)
			rcpt, _ := s.member(t)
			id, err := s.client.Blobs(up).Create(ctxT(t), rcpt.Public(), 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.client.Blobs(up).PutChunk(ctxT(t), id, 0, []byte("x")); err != nil {
				t.Fatal(err)
			}
			advance(core.BlobTTL)
			if _, err := s.client.Blobs(rcpt).GetChunk(ctxT(t), id, 0); httpStatus(err) != http.StatusGone {
				t.Fatalf("expired blob: %v, want 410", err)
			}
		}},
	}
}
