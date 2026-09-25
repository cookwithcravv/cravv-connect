package relayserver

import (
	"context"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/relayproto"
)

// A joiner that claimed the join just as the room was burned must be refused
// instead of attaching to a dead room and waiting forever.
func TestJoinAfterBurnRefused(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	s := tr.srv
	lr, ok := s.rooms.attachCreator("ABCD", nil)
	if !ok {
		t.Fatal("attachCreator failed")
	}
	lr.mu.Unlock()
	s.burnRoom(context.Background(), lr, nil, relayproto.RoomSignal{T: relayproto.TypeClosed}, 1000)
	if err := lr.join(context.Background(), nil); !errors.Is(err, errRoomBurned) {
		t.Fatalf("join on burned room err = %v, want errRoomBurned", err)
	}
}
