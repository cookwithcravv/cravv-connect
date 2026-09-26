package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// tokenBytes is the entropy of wake and reattach tokens.
const tokenBytes = 32

// newToken returns a random secret for a client and the hash the daemon
// stores. The secret itself is never written to disk.
func newToken() (token, hash string) {
	var b [tokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("daemon: crypto/rand failed: " + err.Error())
	}
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, hashToken(token)
}

// hashToken is the stored form of a token: hex SHA-256. An empty token
// hashes to "" so it can never match a stored hash.
func hashToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
