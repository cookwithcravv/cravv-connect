package keys

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func newSigned(t *testing.T) (*Identity, *Prekey, SignedPrekey) {
	t.Helper()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := GeneratePrekey(time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id, pk, pk.Signed(id)
}

func TestSignedPrekeyVerifies(t *testing.T) {
	id, pk, s := newSigned(t)
	if err := s.Verify(id.Public()); err != nil {
		t.Fatalf("valid prekey rejected: %v", err)
	}
	if s.ID != pk.ID || len(s.Pub) != 32 || s.CreatedAt != pk.CreatedAt.UnixMilli() {
		t.Fatalf("unexpected signed prekey %+v", s)
	}
}

func TestSignedPrekeyTamperRejected(t *testing.T) {
	id, _, s := newSigned(t)
	other, _ := GenerateIdentity()
	otherPK, _ := GeneratePrekey(time.Unix(0, 0))
	tests := []struct {
		name string
		mut  func(s SignedPrekey) SignedPrekey
		ik   func() []byte
	}{
		{"sig", func(s SignedPrekey) SignedPrekey { s.Sig = flip(s.Sig); return s }, nil},
		{"pub", func(s SignedPrekey) SignedPrekey { s.Pub = otherPK.Priv.PublicKey().Bytes(); return s }, nil},
		{"created_at", func(s SignedPrekey) SignedPrekey { s.CreatedAt++; return s }, nil},
		{"id", func(s SignedPrekey) SignedPrekey { s.ID = core.NewID(); return s }, nil},
		{"empty id", func(s SignedPrekey) SignedPrekey { s.ID = ""; return s }, nil},
		{"short pub", func(s SignedPrekey) SignedPrekey { s.Pub = s.Pub[:31]; return s }, nil},
		{"wrong identity", func(s SignedPrekey) SignedPrekey { return s }, func() []byte { return other.Public() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ik := id.Public()
			if tt.ik != nil {
				ik = tt.ik()
			}
			cp := s
			cp.Pub = append([]byte(nil), s.Pub...)
			cp.Sig = append([]byte(nil), s.Sig...)
			err := tt.mut(cp).Verify(ik)
			if !errors.Is(err, ErrBadPrekeySignature) {
				t.Fatalf("Verify = %v, want ErrBadPrekeySignature", err)
			}
		})
	}
}

func TestPrekeyFromBytesRoundTrip(t *testing.T) {
	id, pk, s := newSigned(t)
	back, err := PrekeyFromBytes(pk.ID, pk.Priv.Bytes(), pk.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	s2 := back.Signed(id)
	if !bytes.Equal(s2.Pub, s.Pub) || s2.ID != s.ID || s2.CreatedAt != s.CreatedAt {
		t.Fatal("restored prekey differs")
	}
	if _, err := PrekeyFromBytes("x", []byte{1, 2, 3}, time.Time{}); err == nil {
		t.Fatal("short private key accepted")
	}
}

func TestSignedPrekeyWireRoundTrip(t *testing.T) {
	id, _, s := newSigned(t)
	back := SignedPrekeyFromWire(s.Wire())
	if back.ID != s.ID || !bytes.Equal(back.Pub, s.Pub) || back.CreatedAt != s.CreatedAt || !bytes.Equal(back.Sig, s.Sig) {
		t.Fatal("wire round trip changed the prekey")
	}
	if err := back.Verify(id.Public()); err != nil {
		t.Fatal(err)
	}
}

func TestSigningStringExact(t *testing.T) {
	s := SignedPrekey{ID: "PK1", Pub: []byte{0xfb, 0xff}, CreatedAt: 1700000000000}
	want := "cravv-prekey-v1\nPK1\n+/8=\n1700000000000"
	if got := string(s.signingBytes()); got != want {
		t.Fatalf("signing string = %q, want %q", got, want)
	}
}

func flip(b []byte) []byte {
	c := append([]byte(nil), b...)
	c[0] ^= 1
	return c
}
