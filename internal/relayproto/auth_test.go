package relayproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/keys"
)

func TestAuthMessageExactBytes(t *testing.T) {
	got := string(AuthMessage("https://relay.example.com", "bm9uY2U"))
	want := "cravv-relay-auth-v1\nhttps://relay.example.com\nbm9uY2U"
	if got != want {
		t.Fatalf("AuthMessage = %q, want %q", got, want)
	}
}

func TestAuthMessageBindsOrigin(t *testing.T) {
	a := AuthMessage("https://a.example", "n")
	b := AuthMessage("https://b.example", "n")
	if bytes.Equal(a, b) {
		t.Fatal("different origins produced the same signing string")
	}
}

func TestMailboxIDMatchesMachineID(t *testing.T) {
	for i := 0; i < 5; i++ {
		id, err := keys.GenerateIdentity()
		if err != nil {
			t.Fatal(err)
		}
		got := MailboxID(id.Public())
		if got != string(keys.MachineIDOf(id.Public())) {
			t.Fatalf("MailboxID %q != MachineIDOf %q", got, keys.MachineIDOf(id.Public()))
		}
		if len(got) != 52 {
			t.Fatalf("len = %d, want 52", len(got))
		}
	}
}

func TestB64RoundTripAndLenientDecode(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 31, 32, 33, 64} {
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		s := B64(b)
		if len(s) > 0 && s[len(s)-1] == '=' {
			t.Fatalf("B64 emitted padding: %q", s)
		}
		got, err := UnB64(s)
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("unpadded round trip n=%d: %v", n, err)
		}
		padded := base64.StdEncoding.EncodeToString(b)
		got, err = UnB64(padded)
		if err != nil || !bytes.Equal(got, b) {
			t.Fatalf("padded input n=%d: %v", n, err)
		}
	}
	if _, err := UnB64("not base64!"); err == nil {
		t.Fatal("UnB64 accepted garbage")
	}
}

func TestParseIK(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	got, err := ParseIK(B64(pub))
	if err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("ParseIK valid: %v", err)
	}
	for _, bad := range []string{"", B64(pub[:31]), B64(append([]byte{}, append(pub, 0)...)), "%%%"} {
		if _, err := ParseIK(bad); !errors.Is(err, ErrBadIK) {
			t.Fatalf("ParseIK(%q) err = %v, want ErrBadIK", bad, err)
		}
	}
}
