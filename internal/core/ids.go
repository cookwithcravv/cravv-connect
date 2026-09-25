package core

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"strings"
	"time"
)

// crockford is the Crockford base32 alphabet (no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// IDLen is the length of every ID returned by NewID.
const IDLen = 26

// NewID returns a 26-character, time-sortable identifier: 48 bits of unix
// milliseconds followed by 80 random bits, encoded as upper-case Crockford
// base32. IDs created in later milliseconds sort after earlier ones.
func NewID() string { return NewIDAt(SystemClock{}) }

// NewIDAt is NewID with the timestamp taken from clock, so an ID made for
// an envelope encodes the same millisecond as the envelope's TS.
func NewIDAt(clock Clock) string {
	id, err := newIDAt(clock.Now(), rand.Reader)
	if err != nil {
		// crypto/rand.Reader never returns an error on supported platforms.
		panic("core: crypto/rand failed: " + err.Error())
	}
	return id
}

// newIDAt builds an ID for time t using randomness from r.
func newIDAt(t time.Time, r io.Reader) (string, error) {
	var b [16]byte
	ms := uint64(t.UnixMilli()) & (1<<48 - 1)
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", err
	}
	hi := binary.BigEndian.Uint64(b[0:8])
	lo := binary.BigEndian.Uint64(b[8:16])
	var out [IDLen]byte
	for i := IDLen - 1; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}

// ValidID reports whether s has the form NewID produces: exactly IDLen
// upper-case Crockford base32 characters. Every message, task and file ID a
// peer sends must pass it; anything else is rejected on receipt, so a peer
// cannot smuggle control characters or look-alike text into local displays.
func ValidID(s string) bool {
	if len(s) != IDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(crockford, s[i]) < 0 {
			return false
		}
	}
	return true
}

// MaxBlobIDLen is the longest blob ID a relay may issue (relay-v1 section 6.2).
const MaxBlobIDLen = 64

// ValidBlobID reports whether s is a blob ID a relay may issue under
// relay-v1: 1 to 64 characters from [a-z0-9]. Reference relays issue 26
// lower-case base32 characters, which always pass.
func ValidBlobID(s string) bool {
	if s == "" || len(s) > MaxBlobIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
