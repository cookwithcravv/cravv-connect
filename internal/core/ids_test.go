package core

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var idPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

func TestNewIDCharsetAndLength(t *testing.T) {
	for range 1000 {
		id := NewID()
		if !idPattern.MatchString(id) {
			t.Fatalf("NewID() = %q, want 26 Crockford base32 chars", id)
		}
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := make(map[string]bool, 10000)
	for range 10000 {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = true
	}
}

func TestNewIDSortsByTime(t *testing.T) {
	base := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := range 200 {
		// Random bytes of 0xFF for early IDs and 0x00 for later ones prove the
		// timestamp prefix, not the random suffix, decides the order.
		fill := byte(0xFF)
		if i%2 == 1 {
			fill = 0x00
		}
		id, err := newIDAt(base.Add(time.Duration(i)*time.Millisecond), bytes.NewReader(bytes.Repeat([]byte{fill}, 10)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("IDs are not sorted by creation time: %v", ids)
	}
}

func TestNewIDKnownEncoding(t *testing.T) {
	zero := time.UnixMilli(0)
	id, err := newIDAt(zero, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if id != "00000000000000000000000000" {
		t.Fatalf("all-zero ID = %q", id)
	}
	id, err = newIDAt(time.UnixMilli(1<<48-1), bytes.NewReader(bytes.Repeat([]byte{0xFF}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	// 128 one-bits in 130 bit slots: the first char holds only 3 bits.
	if id != "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Fatalf("all-ones ID = %q", id)
	}
}

func TestNewIDRandomFailure(t *testing.T) {
	if _, err := newIDAt(time.UnixMilli(0), bytes.NewReader(nil)); err == nil {
		t.Fatal("expected an error when randomness is unavailable")
	}
}

func TestNewIDAtUsesClock(t *testing.T) {
	at := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	want, err := newIDAt(at, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	id := NewIDAt(NewFakeClock(at))
	if !idPattern.MatchString(id) {
		t.Fatalf("NewIDAt = %q", id)
	}
	// The first 9 characters (45 bits) are pure timestamp.
	if id[:9] != want[:9] {
		t.Fatalf("NewIDAt prefix = %q, want %q", id[:9], want[:9])
	}
}

func TestValidID(t *testing.T) {
	for range 100 {
		if id := NewID(); !ValidID(id) {
			t.Fatalf("ValidID(%q) = false for a NewID", id)
		}
	}
	bad := []string{
		"", "T1", "01j8zr0a1b2c3d4e5f6g7h8j9k", // lower case
		"01J8ZR0A1B2C3D4E5F6G7H8J9",   // 25 chars
		"01J8ZR0A1B2C3D4E5F6G7H8J9KK", // 27 chars
		"01J8ZR0A1B2C3D4E5F6G7H8JIL",  // I and L are not Crockford
		"01J8ZR0A1B2C3D4E5F6G7H8J\n\x1b",
		"01J8ZR0A1B2C3D4E5F6\x1b[8mXX",
		"01J8ZR0A1B2C3D4E5F6G7H8J9\u202e",
	}
	for _, s := range bad {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true", s)
		}
	}
}

func TestValidBlobID(t *testing.T) {
	good := []string{"q2w3e4r5t6y7u8i9o0p1a2s3d4", "abc", strings.Repeat("a", 64)}
	for _, s := range good {
		if !ValidBlobID(s) {
			t.Errorf("ValidBlobID(%q) = false", s)
		}
	}
	bad := []string{"", "ABC", "a/b", "a\nb", "a\x1b[8m", strings.Repeat("a", 65), "ab.c"}
	for _, s := range bad {
		if ValidBlobID(s) {
			t.Errorf("ValidBlobID(%q) = true", s)
		}
	}
}

func TestIDTime(t *testing.T) {
	at := time.Date(2026, 9, 28, 12, 34, 56, 789_000_000, time.UTC)
	id := NewIDAt(NewFakeClock(at))
	got, ok := IDTime(id)
	if !ok || !got.Equal(at) {
		t.Fatalf("IDTime(%s) = %v, %v; want %v", id, got, ok, at)
	}
	for _, bad := range []string{"", "short", strings.Repeat("Z", IDLen), "01ARZ3NDEKTSV4RRFFQ69G5FA!"} {
		if _, ok := IDTime(bad); ok {
			t.Errorf("IDTime(%q) ok, want not an ID", bad)
		}
	}
}
