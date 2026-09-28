package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/pake"
)

// joinByHand runs the joiner's side of the exchange step by step, sending
// payload with the binding signature sign returns (nil sends none, as
// cravv-connect 0.2.1 did). It returns the first failure.
func joinByHand(t *testing.T, rooms *memRooms, code string, payload pairPayload, sign func(key []byte) []byte) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := bindcode.Parse(code)
	if err != nil {
		t.Fatal(err)
	}
	room, err := rooms.Open(ctx, c.Nameplate, "")
	if err != nil {
		return err
	}
	defer room.Close()
	ex, err := pake.SPAKE2{}.New([]byte(c.String()), pake.SideB)
	if err != nil {
		t.Fatal(err)
	}
	if err := room.Send(ctx, ex.Message()); err != nil {
		return err
	}
	msg, err := room.Recv(ctx)
	if err != nil {
		return err
	}
	key, err := ex.Finish(msg)
	if err != nil {
		return err
	}
	if err := room.Send(ctx, pake.ConfirmTag(key, pake.SideB)); err != nil {
		return err
	}
	if _, err := room.Recv(ctx); err != nil {
		return err
	}
	if sign != nil {
		payload.BindSig = sign(key)
	}
	aead, _ := chacha20poly1305.New(pake.SessionKey(key))
	plain, _ := json.Marshal(payload)
	if err := room.Send(ctx, aead.Seal(nil, pairNonce(pake.SideB, 0), plain, pairAAD)); err != nil {
		return err
	}
	if _, err := room.Recv(ctx); err != nil {
		return err
	}
	if err := room.Send(ctx, aead.Seal(nil, pairNonce(pake.SideB, 1), pairAck, pairAAD)); err != nil {
		return err
	}
	_, err = room.Recv(ctx)
	return err
}

// signBind is how an honest joiner holding id signs the binding.
func signBind(id *keys.Identity) func(key []byte) []byte {
	return func(key []byte) []byte { return id.Sign(pake.BindTranscript(key, pake.SideB)) }
}

// The by-hand joiner pairs when it follows the protocol, so the refusals
// below come from what they change and nothing else.
func TestPairingByHandJoinerPairs(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "a", true)
	b := newPairSide(t, rooms, "b", true)
	pendingA, code, err := a.svc.Start(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := b.svc.ownPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := joinByHand(t, rooms, code, mine, signBind(b.id)); err != nil {
		t.Fatalf("joiner: %v", err)
	}
	if prop, err := a.svc.Await(ctx, pendingA); err != nil || prop.MachineID != b.id.MachineID() {
		t.Fatalf("creator: %+v %v", prop, err)
	}
}

// Unknown key share: a joiner who knows the code presents another
// machine's identity key and its (public, validly signed) prekey. It cannot
// sign the binding with that key, so the creator refuses it.
func TestPairingRejectsBorrowedIdentityKey(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "a", true)
	victim := newPairSide(t, rooms, "victim", true)
	attacker := newPairSide(t, rooms, "attacker", true)
	pendingA, code, err := a.svc.Start(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := victim.svc.ownPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := joinByHand(t, rooms, code, borrowed, signBind(attacker.id)); err == nil {
		t.Fatal("the creator acknowledged a borrowed identity key")
	}
	if _, err := a.svc.Await(ctx, pendingA); !errors.Is(err, ErrPairingFailed) {
		t.Fatalf("creator err = %v, want ErrPairingFailed", err)
	}
	if len(a.peers.m) != 0 {
		t.Fatal("a peer was stored")
	}
}

// A signature made for the other side cannot be replayed as this side's.
func TestPairingRejectsBindingForTheWrongSide(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "a", true)
	b := newPairSide(t, rooms, "b", true)
	pendingA, code, err := a.svc.Start(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := b.svc.ownPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	asCreator := func(key []byte) []byte { return b.id.Sign(pake.BindTranscript(key, pake.SideA)) }
	if err := joinByHand(t, rooms, code, mine, asCreator); err == nil {
		t.Fatal("the creator acknowledged a binding signed for side A")
	}
	if _, err := a.svc.Await(ctx, pendingA); !errors.Is(err, ErrPairingFailed) {
		t.Fatalf("creator err = %v, want ErrPairingFailed", err)
	}
}

// A peer on cravv-connect 0.2.1 sends no binding: refused, and the human is
// told to update both machines.
func TestPairingRefusesPeerWithoutBinding(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "a", true)
	old := newPairSide(t, rooms, "old", true)
	pendingA, code, err := a.svc.Start(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := old.svc.ownPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := joinByHand(t, rooms, code, mine, nil); err == nil {
		t.Fatal("the creator acknowledged a payload without a binding")
	}
	_, err = a.svc.Await(ctx, pendingA)
	if !errors.Is(err, ErrPairingPeerOutdated) || !strings.Contains(err.Error(), "update cravv-connect on both machines") {
		t.Fatalf("creator err = %v, want ErrPairingPeerOutdated", err)
	}
}
