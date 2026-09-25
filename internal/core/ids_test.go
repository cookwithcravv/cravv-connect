package core

import (
	"bytes"
	"regexp"
	"sort"
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
