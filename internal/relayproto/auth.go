package relayproto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// AuthContext prefixes the mailbox challenge signing string.
const AuthContext = "cravv-relay-auth-v1"

// ErrBadIK is returned when a B64 string is not a 32-byte Ed25519 public key.
var ErrBadIK = errors.New("relayproto: invalid identity key")

// AuthMessage is the byte string a client signs to answer a challenge.
// origin is scheme://host[:port] of the relay as the client dialed it, with no path.
func AuthMessage(origin, nonce string) []byte {
	return []byte(AuthContext + "\n" + origin + "\n" + nonce)
}

// MailboxID is the lowercase, unpadded RFC 4648 base32 of SHA-256(ik): 52 chars.
// It equals keys.MachineIDOf(ik).
func MailboxID(ik ed25519.PublicKey) string {
	h := sha256.Sum256(ik)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:]))
}

// B64 encodes with unpadded standard base64 (RawStdEncoding).
func B64(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }

// UnB64 decodes standard base64 and accepts input with or without "=" padding.
func UnB64(s string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// ParseIK decodes a B64 identity key and checks its length.
func ParseIK(s string) (ed25519.PublicKey, error) {
	b, err := UnB64(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadIK, err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadIK, len(b))
	}
	return ed25519.PublicKey(b), nil
}
