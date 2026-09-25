// Package bindcode generates and parses one-time pairing codes of the form
// CRAVV-NNNN-SSSS-SSSS (Crockford base32).
package bindcode

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

// Alphabet is Crockford base32: no I, L, O, or U.
const Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	prefix       = "CRAVV"
	NameplateLen = 4
	SecretLen    = 8 // 40 bits
)

// ErrInvalid is returned for any code that cannot be parsed.
var ErrInvalid = errors.New("invalid bind code (expected CRAVV-XXXX-XXXX-XXXX)")

// Code is a parsed bind code. Nameplate is visible to the relay; Secret is
// the PAKE password and never leaves the two machines.
type Code struct{ Nameplate, Secret string }

// NewCode returns a code for the relay-assigned nameplate with a random secret.
func NewCode(nameplate string) (Code, error) {
	np, ok := normalize(nameplate)
	if !ok || len(np) != NameplateLen {
		return Code{}, fmt.Errorf("bindcode: nameplate %q must be %d Crockford base32 characters", nameplate, NameplateLen)
	}
	secret, err := randomSecret()
	if err != nil {
		return Code{}, err
	}
	return Code{Nameplate: np, Secret: secret}, nil
}

// randomSecret encodes 5 random bytes (40 bits) as 8 Crockford characters.
func randomSecret() (string, error) {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("bindcode: random: %w", err)
	}
	v := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	out := make([]byte, SecretLen)
	for i := SecretLen - 1; i >= 0; i-- {
		out[i] = Alphabet[v&31]
		v >>= 5
	}
	return string(out), nil
}

// String renders "CRAVV-NNNN-SSSS-SSSS", or "CRAVV-invalid" for a Code
// whose fields have the wrong lengths (such as the zero Code).
func (c Code) String() string {
	if len(c.Nameplate) != NameplateLen || len(c.Secret) != SecretLen {
		return prefix + "-invalid"
	}
	return prefix + "-" + c.Nameplate + "-" + c.Secret[:4] + "-" + c.Secret[4:]
}

// Parse accepts a code in any case, with or without spaces and hyphens,
// reading O as 0 and I or L as 1. U and other characters are refused, as is
// any non-ASCII byte (checked before upper-casing, since Unicode case mapping
// turns letters such as U+0131 and U+017F into ASCII I and S).
func Parse(s string) (Code, error) {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return Code{}, ErrInvalid
		}
	}
	s = strings.ToUpper(strings.Join(strings.Fields(s), ""))
	if !strings.HasPrefix(s, prefix) {
		return Code{}, ErrInvalid
	}
	body, ok := normalize(strings.ReplaceAll(s[len(prefix):], "-", ""))
	if !ok || len(body) != NameplateLen+SecretLen {
		return Code{}, ErrInvalid
	}
	return Code{Nameplate: body[:NameplateLen], Secret: body[NameplateLen:]}, nil
}

// normalize upper-cases s, maps O to 0 and I/L to 1, and reports whether
// every character is then in Alphabet.
func normalize(s string) (string, bool) {
	b := []byte(s)
	for i, c := range b {
		if c >= 0x80 {
			return "", false
		}
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
			b[i] = c
		}
		switch c {
		case 'O':
			b[i] = '0'
		case 'I', 'L':
			b[i] = '1'
		}
		if !strings.ContainsRune(Alphabet, rune(b[i])) {
			return "", false
		}
	}
	return string(b), true
}
