package sealing

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
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

// encodeEnvelope is the sealed plaintext: env as JSON without HTML escaping
// (so '<', '>' and '&' cost one byte, not six) and without a trailing newline.
func encodeEnvelope(env core.Envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return nil, fmt.Errorf("sealing: marshal envelope: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// DefaultSuite overhead: 32-byte X25519 encapsulated key plus 16-byte tag.
const (
	defaultSuiteEncLen = 32
	defaultSuiteTagLen = 16
	sigLen             = ed25519.SignatureSize
)

// FitsFrame reports, without encrypting, whether Seal would produce a frame
// of at most core.MaxFrameBytes for env. It returns an error wrapping
// core.ErrTooLarge if not. It assumes the longest allowed pk_id (MaxIDLen),
// so it is exact for such a prekey and conservative for shorter ones: when
// FitsFrame returns nil, Seal with DefaultSuite will not fail for size.
func FitsFrame(env core.Envelope) error {
	pt, err := encodeEnvelope(env)
	if err != nil {
		return err
	}
	dummy := Frame{
		Header: Header{
			V:           FrameVersion,
			ID:          env.ID,
			FromMachine: env.FromMachine,
			ToMachine:   env.ToMachine,
			PKID:        strings.Repeat("P", MaxIDLen),
			Suite:       DefaultSuite,
		},
		Payload: make([]byte, defaultSuiteEncLen+len(pt)+defaultSuiteTagLen),
		Sig:     make([]byte, sigLen),
	}
	return checkFrameSize(dummy)
}

// checkFrameSize marshals f and fails with core.ErrTooLarge if it exceeds
// core.MaxFrameBytes.
func checkFrameSize(f Frame) error {
	raw, err := f.Marshal()
	if err != nil {
		return err
	}
	if len(raw) > core.MaxFrameBytes {
		return fmt.Errorf("sealing: frame would be %d bytes (max %d): %w", len(raw), core.MaxFrameBytes, core.ErrTooLarge)
	}
	return nil
}

// Seal encrypts env to the recipient's signed prekey with DefaultSuite and
// signs the frame with the sender's identity. The caller must have verified
// `to` against the recipient's identity key when it was received. It fails
// with ErrMalformed if a header field would not pass ParseFrame and with
// core.ErrTooLarge if the marshalled frame would exceed core.MaxFrameBytes.
func Seal(sender *keys.Identity, to keys.SignedPrekey, env core.Envelope) (Frame, error) {
	if env.FromMachine != sender.MachineID() {
		return Frame{}, fmt.Errorf("sealing: envelope from %s but sender is %s: %w", env.FromMachine, sender.MachineID(), ErrMismatch)
	}
	suite, ok := Lookup(DefaultSuite)
	if !ok {
		return Frame{}, ErrUnknownSuite
	}
	pt, err := encodeEnvelope(env)
	if err != nil {
		return Frame{}, err
	}
	h := Header{
		V:           FrameVersion,
		ID:          env.ID,
		FromMachine: env.FromMachine,
		ToMachine:   env.ToMachine,
		PKID:        to.ID,
		Suite:       suite.ID(),
	}
	if err := h.validate(); err != nil {
		return Frame{}, err
	}
	payload, err := suite.Seal(to.Pub, h.info(), pt)
	if err != nil {
		return Frame{}, err
	}
	f := Frame{Header: h, Payload: payload, Sig: sender.Sign(signingBytes(h, payload))}
	if err := checkFrameSize(f); err != nil {
		return Frame{}, err
	}
	return f, nil
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
