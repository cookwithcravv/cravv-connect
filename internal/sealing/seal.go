package sealing

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
)

// PrekeyResolver finds this machine's private prekey by ID, including
// superseded prekeys that are still retained.
type PrekeyResolver interface {
	PrivatePrekey(id string) (*ecdh.PrivateKey, bool)
}

var (
	ErrUnknownPrekey = errors.New("unknown or deleted prekey")
	ErrBadSignature  = errors.New("bad signature")
	ErrMismatch      = errors.New("header/envelope mismatch")
	ErrUnknownSuite  = errors.New("unknown suite")
)

// Seal encrypts env to the recipient's signed prekey with DefaultSuite and
// signs the frame with the sender's identity. The caller must have verified
// `to` against the recipient's identity key when it was received.
func Seal(sender *keys.Identity, to keys.SignedPrekey, env core.Envelope) (Frame, error) {
	if env.FromMachine != sender.MachineID() {
		return Frame{}, fmt.Errorf("sealing: envelope from %s but sender is %s: %w", env.FromMachine, sender.MachineID(), ErrMismatch)
	}
	suite, ok := Lookup(DefaultSuite)
	if !ok {
		return Frame{}, ErrUnknownSuite
	}
	pt, err := json.Marshal(env)
	if err != nil {
		return Frame{}, fmt.Errorf("sealing: marshal envelope: %w", err)
	}
	h := Header{
		V:           FrameVersion,
		ID:          env.ID,
		FromMachine: env.FromMachine,
		ToMachine:   env.ToMachine,
		PKID:        to.ID,
		Suite:       suite.ID(),
	}
	payload, err := suite.Seal(to.Pub, h.info(), pt)
	if err != nil {
		return Frame{}, err
	}
	return Frame{Header: h, Payload: payload, Sig: sender.Sign(signingBytes(h, payload))}, nil
}

// Open verifies and decrypts a frame from a paired peer whose identity key
// is senderIK. It checks, in order: the signature, that the header's sender
// is the signer and its recipient is local, the suite, the prekey, the
// decryption, and that the inner envelope matches the header exactly.
// Timestamp and replay checks belong to the caller.
func Open(f Frame, senderIK ed25519.PublicKey, local core.MachineID, pr PrekeyResolver) (core.Envelope, error) {
	h := f.Header
	if !keys.Verify(senderIK, signingBytes(h, f.Payload), f.Sig) {
		return core.Envelope{}, ErrBadSignature
	}
	signer := keys.MachineIDOf(senderIK)
	if h.FromMachine != signer {
		return core.Envelope{}, fmt.Errorf("sealing: header sender %s is not signer %s: %w", h.FromMachine, signer, ErrMismatch)
	}
	if h.ToMachine != local {
		return core.Envelope{}, fmt.Errorf("sealing: frame is for %s, not %s: %w", h.ToMachine, local, ErrMismatch)
	}
	suite, ok := Lookup(h.Suite)
	if !ok {
		return core.Envelope{}, fmt.Errorf("sealing: %q: %w", h.Suite, ErrUnknownSuite)
	}
	priv, ok := pr.PrivatePrekey(h.PKID)
	if !ok {
		return core.Envelope{}, fmt.Errorf("sealing: prekey %s: %w", h.PKID, ErrUnknownPrekey)
	}
	pt, err := suite.Open(priv, h.info(), f.Payload)
	if err != nil {
		return core.Envelope{}, err
	}
	var env core.Envelope
	if err := json.Unmarshal(pt, &env); err != nil {
		return core.Envelope{}, fmt.Errorf("sealing: parse envelope: %w", err)
	}
	if env.V != core.EnvelopeVersion || env.ID != h.ID || env.FromMachine != h.FromMachine || env.ToMachine != h.ToMachine {
		return core.Envelope{}, ErrMismatch
	}
	return env, nil
}
