package sealing

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRFC9180VectorA2 checks the exact primitive combination hpkeSuite uses
// (DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, ChaCha20-Poly1305, Base mode)
// against RFC 9180 Appendix A.2.1, sequence number 0.
func TestRFC9180VectorA2(t *testing.T) {
	skR, err := ecdh.X25519().NewPrivateKey(unhex(t, "8057991eef8f1f1af18f4a9491d16a1ce333f695d4db8e38da75975c4478e0fb"))
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(skR.PublicKey().Bytes()); got != "4310ee97d88cc1f088a5576c77ab0cf5c3ac797f3d95139c6c84b5429c59662a" {
		t.Fatalf("pkRm = %s", got)
	}
	priv, err := hpke.NewDHKEMPrivateKey(skR)
	if err != nil {
		t.Fatal(err)
	}
	enc := unhex(t, "1afa08d3dec047a643885163f1180476fa7ddb54c6a8029ea33f95796bf2ac4a")
	info := unhex(t, "4f6465206f6e2061204772656369616e2055726e")
	r, err := hpke.NewRecipient(enc, priv, hpke.HKDFSHA256(), hpke.ChaCha20Poly1305(), info)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := r.Open(unhex(t, "436f756e742d30"), unhex(t, "1c5250d8034ec2b784ba2cfd69dbdb8af406cfe3ff938e131f0def8c8b60b4db21993c62ce81883d2dd1b51a28"))
	if err != nil {
		t.Fatal(err)
	}
	if want := unhex(t, "4265617574792069732074727574682c20747275746820626561757479"); !bytes.Equal(pt, want) {
		t.Fatalf("pt = %x, want %x", pt, want)
	}
}

func TestHPKESuiteRoundTripAndInfoBinding(t *testing.T) {
	s, ok := Lookup(DefaultSuite)
	if !ok {
		t.Fatal("default suite not registered")
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := s.Seal(priv.PublicKey().Bytes(), []byte("info-1"), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != 32+len("secret")+16 {
		t.Fatalf("payload length %d, want enc(32)+pt+tag(16)", len(ct))
	}
	pt, err := s.Open(priv, []byte("info-1"), ct)
	if err != nil || string(pt) != "secret" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	if _, err := s.Open(priv, []byte("info-2"), ct); err == nil {
		t.Fatal("payload opened under a different info")
	}
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	if _, err := s.Open(other, []byte("info-1"), ct); err == nil {
		t.Fatal("payload opened with the wrong private key")
	}
	if _, err := s.Seal([]byte{1, 2, 3}, nil, []byte("x")); err == nil {
		t.Fatal("malformed recipient key accepted")
	}
}
