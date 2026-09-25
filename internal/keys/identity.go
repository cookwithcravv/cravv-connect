// Package keys holds the long-term Ed25519 identity and the signed X25519 prekeys.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
)

// Identity is a machine's Ed25519 identity key (IK).
type Identity struct {
	priv ed25519.PrivateKey
}

// GenerateIdentity creates a new random identity.
func GenerateIdentity() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keys: generate identity: %w", err)
	}
	return &Identity{priv: priv}, nil
}

// IdentityFromSeed rebuilds an identity from its 32-byte seed.
func IdentityFromSeed(seed []byte) (*Identity, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("keys: identity seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return &Identity{priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// Seed returns a copy of the 32-byte private seed (for storage).
func (i *Identity) Seed() []byte {
	return append([]byte(nil), i.priv.Seed()...)
}

// Public returns the identity public key.
func (i *Identity) Public() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), i.priv.Public().(ed25519.PublicKey)...)
}

// MachineID returns the machine ID derived from the public key.
func (i *Identity) MachineID() core.MachineID { return MachineIDOf(i.Public()) }

// Sign signs msg with the identity key.
func (i *Identity) Sign(msg []byte) []byte { return ed25519.Sign(i.priv, msg) }

var machineIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// MachineIDOf is lower-case unpadded base32 of SHA-256(pub): 52 characters.
func MachineIDOf(pub ed25519.PublicKey) core.MachineID {
	sum := sha256.Sum256(pub)
	return core.MachineID(strings.ToLower(machineIDEncoding.EncodeToString(sum[:])))
}

// Verify checks an Ed25519 signature. A malformed public key returns false.
func Verify(pub ed25519.PublicKey, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}
