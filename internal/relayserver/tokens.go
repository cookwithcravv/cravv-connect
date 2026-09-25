package relayserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"strings"
)

// crockford is the Crockford base32 alphabet used for nameplates (same as bind codes).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var lowerB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// randomToken returns 26 lowercase base32 chars carrying 128 random bits.
func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return strings.ToLower(lowerB32.EncodeToString(b))
}

// randomHexToken returns 32 lowercase hex chars carrying 128 random bits.
func randomHexToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomNameplate returns 4 Crockford base32 chars (20 bits).
func randomNameplate() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	out := make([]byte, 4)
	for i := range b {
		out[i] = crockford[b[i]&31]
	}
	return string(out)
}

// randomNonce returns 32 random bytes.
func randomNonce() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

// secretEqual compares secrets in constant time; empty secrets never match.
func secretEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
