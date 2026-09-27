package conformance

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

func roomCases() []testCase {
	return []testCase{
		{"rooms/one_joiner_relay_and_burn", func(t *testing.T, s *suite) {
			_, mb := s.member(t)
			np, tok, err := mb.CreateRoom(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			if len(np) != 4 || tok == "" {
				t.Fatalf("nameplate %q token %q", np, tok)
			}
			creator, err := s.client.Rooms().Open(ctxT(t), np, tok)
			if err != nil {
				t.Fatalf("creator open: %v", err)
			}
			defer creator.Close()
			joiner, err := s.client.Rooms().Open(ctxT(t), np, "")
			if err != nil {
				t.Fatalf("joiner open: %v", err)
			}
			if err := creator.WaitPeer(ctxT(t)); err != nil {
				t.Fatalf("creator wait: %v", err)
			}
			if err := joiner.WaitPeer(ctxT(t)); err != nil {
				t.Fatalf("joiner wait: %v", err)
			}
			if err := creator.Send(ctxT(t), []byte("A1")); err != nil {
				t.Fatal(err)
			}
			if got, err := joiner.Recv(ctxT(t)); err != nil || string(got) != "A1" {
				t.Fatalf("joiner got %q %v", got, err)
			}
			if err := joiner.Send(ctxT(t), []byte("B1")); err != nil {
				t.Fatal(err)
			}
			if got, err := creator.Recv(ctxT(t)); err != nil || string(got) != "B1" {
				t.Fatalf("creator got %q %v", got, err)
			}
			if _, err := s.client.Rooms().Open(ctxT(t), np, ""); !errors.Is(err, transport.ErrRoomGone) {
				t.Fatalf("second joiner err = %v, want room gone", err)
			}
			joiner.Close()
			if _, err := creator.Recv(ctxT(t)); !errors.Is(err, io.EOF) {
				t.Fatalf("creator after joiner left: %v, want EOF", err)
			}
			if _, err := s.client.Rooms().Open(ctxT(t), np, tok); !errors.Is(err, transport.ErrRoomGone) {
				t.Fatalf("reopen burned room err = %v", err)
			}
		}},
		{"rooms/early_messages_buffered_and_nameplate_case_insensitive", func(t *testing.T, s *suite) {
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
			for _, m := range []string{"early-1", "early-2"} {
				if err := creator.Send(ctxT(t), []byte(m)); err != nil {
					t.Fatal(err)
				}
			}
			// No wait needed: whether the relay reads these before or after the join, it
			// must deliver them to the joiner after peer_joined and in order.
			joiner, err := s.client.Rooms().Open(ctxT(t), strings.ToLower(np), "")
			if err != nil {
				t.Fatalf("lowercase nameplate: %v", err)
			}
			defer joiner.Close()
			for _, want := range []string{"early-1", "early-2"} {
				if got, err := joiner.Recv(ctxT(t)); err != nil || string(got) != want {
					t.Fatalf("joiner got %q %v, want %q", got, err, want)
				}
			}
		}},
		{"rooms/non_msg_frame_bad_request_and_peer_closed", func(t *testing.T, s *suite) {
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
			joiner := s.rawPair(t, np, "")
			joiner.expect(relayproto.TypePeerJoined)
			if err := creator.WaitPeer(ctxT(t)); err != nil {
				t.Fatal(err)
			}
			joiner.write(relayproto.RoomSignal{T: relayproto.TypeWaiting})
			joiner.expectError(relayproto.CodeBadRequest)
			if _, err := creator.Recv(ctxT(t)); !errors.Is(err, io.EOF) {
				t.Fatalf("peer after rule break: %v, want closed (EOF)", err)
			}
		}},
		{"rooms/msg_over_262144_too_large", func(t *testing.T, s *suite) {
			_, mb := s.member(t)
			np, tok, err := mb.CreateRoom(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			creator := s.rawPair(t, np, tok)
			creator.expect(relayproto.TypeWaiting)
			creator.write(relayproto.RoomMsg{T: relayproto.TypeMsg, Data: relayproto.B64(make([]byte, core.MaxFrameBytes+1))})
			creator.expectError(relayproto.CodeTooLarge)
		}},
		{"rooms/max_size_msg_relayed", func(t *testing.T, s *suite) {
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
			joiner, err := s.client.Rooms().Open(ctxT(t), np, "")
			if err != nil {
				t.Fatal(err)
			}
			defer joiner.Close()
			big := bytes.Repeat([]byte{7}, core.MaxFrameBytes)
			if err := creator.Send(ctxT(t), big); err != nil {
				t.Fatal(err)
			}
			if got, err := joiner.Recv(ctxT(t)); err != nil || !bytes.Equal(got, big) {
				t.Fatalf("max-size msg: %d bytes, %v", len(got), err)
			}
		}},
		{"rooms/joiner_before_creator_not_consumed", func(t *testing.T, s *suite) {
			_, mb := s.member(t)
			np, tok, err := mb.CreateRoom(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			early := s.rawPair(t, np, "")
			early.expectError(relayproto.CodeNotFound)
			creator, err := s.client.Rooms().Open(ctxT(t), np, tok)
			if err != nil {
				t.Fatalf("creator after early joiner: %v", err)
			}
			defer creator.Close()
			joiner, err := s.client.Rooms().Open(ctxT(t), np, "")
			if err != nil {
				t.Fatalf("the early attempt consumed the join: %v", err)
			}
			defer joiner.Close()
			if err := creator.WaitPeer(ctxT(t)); err != nil {
				t.Fatal(err)
			}
		}},
		{"rooms/second_creator_gone", func(t *testing.T, s *suite) {
			_, mb := s.member(t)
			np, tok, err := mb.CreateRoom(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			first, err := s.client.Rooms().Open(ctxT(t), np, tok)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			if _, err := s.client.Rooms().Open(ctxT(t), np, tok); !errors.Is(err, transport.ErrRoomGone) {
				t.Fatalf("second creator err = %v, want room gone", err)
			}
		}},
		{"rooms/unknown_nameplate", func(t *testing.T, s *suite) {
			if _, err := s.client.Rooms().Open(ctxT(t), "ZZZZ", ""); !errors.Is(err, transport.ErrRoomGone) {
				t.Fatalf("err = %v, want room gone", err)
			}
		}},
		{"rooms/wrong_creator_token", func(t *testing.T, s *suite) {
			_, mb := s.member(t)
			np, _, err := mb.CreateRoom(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.client.Rooms().Open(ctxT(t), np, "wrong-token"); !errors.Is(err, transport.ErrRelayForbidden) {
				t.Fatalf("err = %v, want forbidden", err)
			}
		}},
		{"rooms/creator_leaving_burns_room", func(t *testing.T, s *suite) {
			_, mb := s.member(t)
			np, tok, err := mb.CreateRoom(ctxT(t))
			if err != nil {
				t.Fatal(err)
			}
			creator, err := s.client.Rooms().Open(ctxT(t), np, tok)
			if err != nil {
				t.Fatal(err)
			}
			creator.Close()
			// The relay tears the room down asynchronously after the close; allow it a moment.
			deadline := time.Now().Add(5 * time.Second)
			for {
				j, err := s.client.Rooms().Open(ctxT(t), np, "")
				if errors.Is(err, transport.ErrRoomGone) {
					return
				}
				if err == nil {
					j.Close()
				}
				if time.Now().After(deadline) {
					t.Fatalf("room still joinable after creator left (last err %v)", err)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}},
	}
}
