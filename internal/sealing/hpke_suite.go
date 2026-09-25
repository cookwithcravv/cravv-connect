package sealing

import (
	"crypto/ecdh"
	"crypto/hpke"
	"fmt"
)

// DefaultSuite is HPKE Base mode, DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305.
const DefaultSuite = "hpke-x25519-sha256-chacha20poly1305"

func init() { Register(hpkeSuite{}) }

// hpkeSuite uses the standard library's single-shot HPKE. The single-shot
// API has no AAD, so callers bind context through info. Output is enc || ct.
type hpkeSuite struct{}

func (hpkeSuite) ID() string { return DefaultSuite }

func (hpkeSuite) Seal(recipientPub []byte, info, plaintext []byte) ([]byte, error) {
	pub, err := hpke.DHKEM(ecdh.X25519()).NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("sealing: recipient prekey: %w", err)
	}
	out, err := hpke.Seal(pub, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), info, plaintext)
	if err != nil {
		return nil, fmt.Errorf("sealing: hpke seal: %w", err)
	}
	return out, nil
}

func (hpkeSuite) Open(recipientPriv *ecdh.PrivateKey, info, payload []byte) ([]byte, error) {
	priv, err := hpke.NewDHKEMPrivateKey(recipientPriv)
	if err != nil {
		return nil, fmt.Errorf("sealing: prekey: %w", err)
	}
	pt, err := hpke.Open(priv, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), info, payload)
	if err != nil {
		return nil, fmt.Errorf("sealing: hpke open: %w", err)
	}
	return pt, nil
}
