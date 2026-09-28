package pake

import (
	"crypto/hmac"
	"crypto/sha256"
	"io"

	"golang.org/x/crypto/hkdf"
)

func derive(key []byte, info string) []byte {
	out := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, key, nil, []byte(info)), out); err != nil {
		// HKDF-SHA256 can produce up to 8160 bytes; 32 never fails.
		panic("pake: hkdf: " + err.Error())
	}
	return out
}

// ConfirmTag is HMAC-SHA256(HKDF(key, "cravv-connect/pair-v1/confirm"),
// "A" or "B"). Each side sends the tag for its own side.
func ConfirmTag(key []byte, side Side) []byte {
	m := hmac.New(sha256.New, derive(key, "cravv-connect/pair-v1/confirm"))
	m.Write([]byte{sideByte(side)})
	return m.Sum(nil)
}

// CheckConfirm verifies the tag the peer sent; side is the peer's side.
func CheckConfirm(key []byte, side Side, tag []byte) bool {
	return hmac.Equal(ConfirmTag(key, side), tag)
}

// SessionKey is the 32-byte AEAD key for the pairing payload exchange.
func SessionKey(key []byte) []byte { return derive(key, "cravv-connect/pair-v1/aead") }

// bindDomain separates the identity-key binding signature from every other
// use of the identity key.
const bindDomain = "cravv-connect/pair-v1/bind\n"

// BindTranscript is what the machine on side signer signs with its identity
// key to prove it holds that key in this exchange: the domain, the signer's
// side byte, the other side's byte, and HKDF(key, "cravv-connect/pair-v1/bind").
// Both side bytes are in it, so a signature cannot be reflected back to its
// maker, and the derived key ties it to this one PAKE session.
func BindTranscript(key []byte, signer Side) []byte {
	msg := make([]byte, 0, len(bindDomain)+2+32)
	msg = append(msg, bindDomain...)
	msg = append(msg, sideByte(signer), sideByte(signer.Other()))
	return append(msg, derive(key, "cravv-connect/pair-v1/bind")...)
}
