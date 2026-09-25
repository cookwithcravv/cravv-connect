package keys

import (
	"bytes"
	"crypto/ed25519"
	"regexp"
	"testing"
)

var machineIDPattern = regexp.MustCompile(`^[a-z2-7]{52}$`)

func TestSignVerify(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("hello")
	sig := id.Sign(msg)
	if !Verify(id.Public(), msg, sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify(id.Public(), []byte("hellO"), sig) {
		t.Fatal("signature accepted for a different message")
	}
	bad := append([]byte(nil), sig...)
	bad[0] ^= 1
	if Verify(id.Public(), msg, bad) {
		t.Fatal("tampered signature accepted")
	}
	other, _ := GenerateIdentity()
	if Verify(other.Public(), msg, sig) {
		t.Fatal("signature accepted under another key")
	}
	if Verify(ed25519.PublicKey{1, 2, 3}, msg, sig) {
		t.Fatal("malformed public key must return false, not panic")
	}
}

func TestSeedRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	seed := id.Seed()
	if len(seed) != 32 {
		t.Fatalf("seed length %d, want 32", len(seed))
	}
	back, err := IdentityFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Public(), id.Public()) || back.MachineID() != id.MachineID() {
		t.Fatal("identity from seed differs")
	}
	if !Verify(id.Public(), []byte("m"), back.Sign([]byte("m"))) {
		t.Fatal("restored identity signs differently")
	}
	seed[0] ^= 0xFF
	if !bytes.Equal(id.Seed(), back.Seed()) {
		t.Fatal("Seed() must return a copy")
	}
}

func TestIdentityFromSeedWrongLength(t *testing.T) {
	for _, n := range []int{0, 31, 33, 64} {
		if _, err := IdentityFromSeed(make([]byte, n)); err == nil {
			t.Errorf("seed of %d bytes accepted", n)
		}
	}
}

func TestMachineIDFormat(t *testing.T) {
	id, _ := GenerateIdentity()
	m := id.MachineID()
	if !machineIDPattern.MatchString(string(m)) {
		t.Fatalf("MachineID %q is not 52 lowercase base32 chars", m)
	}
	if MachineIDOf(id.Public()) != m {
		t.Fatal("MachineIDOf disagrees with Identity.MachineID")
	}
	if len(m.Short()) != 16 {
		t.Fatalf("Short() = %q", m.Short())
	}
}

func TestMachineIDDeterministicKnownValue(t *testing.T) {
	id, err := IdentityFromSeed(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	first := id.MachineID()
	again, _ := IdentityFromSeed(make([]byte, 32))
	if again.MachineID() != first {
		t.Fatal("MachineID not deterministic")
	}
	// The all-zero seed has public key 3b6a27bc...8b59da29; this is
	// lower(base32nopad(sha256(pub))), computed independently.
	const want = "copdsqhgjnkjc4rardm2bv2bmkh4qjxasr25gqnhqcwn4pclqbya"
	if first != want {
		t.Fatalf("MachineID = %s, want %s", first, want)
	}
	other, _ := GenerateIdentity()
	if other.MachineID() == first {
		t.Fatal("different keys produced the same MachineID")
	}
}
