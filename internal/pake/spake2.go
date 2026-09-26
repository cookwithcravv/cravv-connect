package pake

import (
	"bytes"
	"errors"
	"fmt"

	"salsa.debian.org/vasudev/gospake2"
)

const (
	identityA = "cravv-connect/pair-v1/A"
	identityB = "cravv-connect/pair-v1/B"
	// spakeMsgLen is one side byte ('A' or 'B') plus a 32-byte Ed25519 element.
	spakeMsgLen = 33
)

// ErrBadMessage is returned when the peer's PAKE message is malformed.
var ErrBadMessage = errors.New("pake: malformed peer message")

// SPAKE2 implements Factory with gospake2's Ed25519 group (the variant
// magic-wormhole and python-spake2 use).
//
// There are no cross-implementation test vectors (for example against
// python-spake2): gospake2 draws its scalar from crypto/rand internally and
// offers no way to inject randomness, so a deterministic transcript cannot be
// reproduced. Wire compatibility is instead pinned by the exact gospake2
// version in go.mod; upgrading it is a protocol change and must be checked
// against a live peer.
type SPAKE2 struct{}

// New starts one side of the exchange.
func (SPAKE2) New(password []byte, side Side) (Exchange, error) {
	if !side.Valid() {
		return nil, fmt.Errorf("pake: invalid side %d", side)
	}
	if len(password) == 0 {
		return nil, errors.New("pake: empty password")
	}
	pw := gospake2.NewPassword(string(password))
	idA := gospake2.NewIdentityA(identityA)
	idB := gospake2.NewIdentityB(identityB)
	var state gospake2.SPAKE2
	if side == SideA {
		state = gospake2.SPAKE2A(pw, idA, idB)
	} else {
		state = gospake2.SPAKE2B(pw, idA, idB)
	}
	x := &spake2Exchange{state: state, side: side}
	x.msg = x.state.Start()
	return x, nil
}

type spake2Exchange struct {
	state    gospake2.SPAKE2
	side     Side
	msg      []byte
	finished bool
}

func (x *spake2Exchange) Message() []byte { return append([]byte(nil), x.msg...) }

func sideByte(s Side) byte {
	if s == SideA {
		return 'A'
	}
	return 'B'
}

// Finish validates the peer message and derives the shared key. It may be
// called once. gospake2 indexes the message without checks, so the length
// and side byte are validated here and any panic is turned into an error.
func (x *spake2Exchange) Finish(peerMsg []byte) (key []byte, err error) {
	if x.finished {
		return nil, errors.New("pake: exchange already finished")
	}
	x.finished = true
	if len(peerMsg) != spakeMsgLen || peerMsg[0] != sideByte(x.side.Other()) {
		return nil, ErrBadMessage
	}
	// A reflected message (our own element sent back) must never produce a
	// key. gospake2 has a reflection check of its own, but it compares
	// encodings that can differ in padding, so compare the element here too.
	if bytes.Equal(peerMsg[1:], x.msg[1:]) {
		return nil, ErrBadMessage
	}
	defer func() {
		if r := recover(); r != nil {
			key, err = nil, fmt.Errorf("%w: %v", ErrBadMessage, r)
		}
	}()
	k, ferr := x.state.Finish(peerMsg)
	if ferr != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadMessage, ferr)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("pake: derived key is %d bytes, want 32", len(k))
	}
	return k, nil
}
