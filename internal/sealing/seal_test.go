package sealing

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
)

type mapResolver map[string]*ecdh.PrivateKey

func (m mapResolver) PrivatePrekey(id string) (*ecdh.PrivateKey, bool) {
	k, ok := m[id]
	return k, ok
}

type fixture struct {
	alice, bob, mallory *keys.Identity
	bobPK               *keys.Prekey
	bobSigned           keys.SignedPrekey
	resolver            mapResolver
	clock               *core.FakeClock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{clock: core.NewFakeClock(time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC))}
	var err error
	for _, p := range []**keys.Identity{&f.alice, &f.bob, &f.mallory} {
		if *p, err = keys.GenerateIdentity(); err != nil {
			t.Fatal(err)
		}
	}
	if f.bobPK, err = keys.GeneratePrekey(f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	f.bobSigned = f.bobPK.Signed(f.bob)
	f.resolver = mapResolver{f.bobPK.ID: f.bobPK.Priv}
	return f
}

func (f *fixture) chat(t *testing.T, from, to *keys.Identity, text string) core.Envelope {
	t.Helper()
	env, err := core.NewEnvelope(f.clock, from.MachineID(), to.MachineID(), core.KindChat, core.ChatBody{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// resign re-signs a frame with id, keeping whatever header and payload it has.
func resign(fr Frame, id *keys.Identity) Frame {
	fr.Sig = id.Sign(signingBytes(fr.Header, fr.Payload))
	return fr
}

func TestSealOpenRoundTrip(t *testing.T) {
	f := newFixture(t)
	env := f.chat(t, f.alice, f.bob, "hello bob")
	env.LinkID = "01JLINK"
	fr, err := Seal(f.alice, f.bobSigned, env)
	if err != nil {
		t.Fatal(err)
	}
	if fr.Header.ID != env.ID || fr.Header.PKID != f.bobPK.ID || fr.Header.Suite != DefaultSuite {
		t.Fatalf("header %+v", fr.Header)
	}
	raw, err := fr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(parsed, f.alice.Public(), f.bob.MachineID(), f.resolver)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(env)
	have, _ := json.Marshal(got)
	if string(want) != string(have) {
		t.Fatalf("envelope changed:\n got %s\nwant %s", have, want)
	}
}

func TestSealRefusesForeignFromMachine(t *testing.T) {
	f := newFixture(t)
	env := f.chat(t, f.mallory, f.bob, "x")
	if _, err := Seal(f.alice, f.bobSigned, env); !errors.Is(err, ErrMismatch) {
		t.Fatalf("Seal = %v, want ErrMismatch", err)
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	f := newFixture(t)
	fr, err := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		mut  func(Frame) Frame
	}{
		{"payload", func(x Frame) Frame { x.Payload = flipped(x.Payload, len(x.Payload)-1); return x }},
		{"enc", func(x Frame) Frame { x.Payload = flipped(x.Payload, 0); return x }},
		{"header id", func(x Frame) Frame { x.Header.ID = core.NewID(); return x }},
		{"header pk_id", func(x Frame) Frame { x.Header.PKID = "other"; return x }},
		{"header suite", func(x Frame) Frame { x.Header.Suite = "other"; return x }},
		{"sig", func(x Frame) Frame { x.Sig = flipped(x.Sig, 0); return x }},
		{"empty sig", func(x Frame) Frame { x.Sig = nil; return x }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Open(tt.mut(fr), f.alice.Public(), f.bob.MachineID(), f.resolver)
			if !errors.Is(err, ErrBadSignature) {
				t.Fatalf("Open = %v, want ErrBadSignature", err)
			}
		})
	}
}

func TestOpenWrongSenderKey(t *testing.T) {
	f := newFixture(t)
	fr, _ := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "hi"))
	if _, err := Open(fr, f.mallory.Public(), f.bob.MachineID(), f.resolver); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Open = %v, want ErrBadSignature", err)
	}
}

func TestOpenToMachineMismatch(t *testing.T) {
	f := newFixture(t)
	// Alice seals to Bob's prekey but addresses Mallory's machine.
	fr, err := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.mallory, "misrouted"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(fr, f.alice.Public(), f.bob.MachineID(), f.resolver); !errors.Is(err, ErrMismatch) {
		t.Fatalf("Open = %v, want ErrMismatch", err)
	}
}

func TestOpenUnknownPrekey(t *testing.T) {
	f := newFixture(t)
	fr, _ := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "hi"))
	if _, err := Open(fr, f.alice.Public(), f.bob.MachineID(), mapResolver{}); !errors.Is(err, ErrUnknownPrekey) {
		t.Fatalf("Open = %v, want ErrUnknownPrekey", err)
	}
}

func TestOpenSupersededPrekeyStillWorks(t *testing.T) {
	f := newFixture(t)
	fr, _ := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "sealed before rotation"))
	newer, err := keys.GeneratePrekey(f.clock.Now().Add(core.PrekeyRotation))
	if err != nil {
		t.Fatal(err)
	}
	f.resolver[newer.ID] = newer.Priv // old key is still retained
	if _, err := Open(fr, f.alice.Public(), f.bob.MachineID(), f.resolver); err != nil {
		t.Fatalf("retained prekey must still open: %v", err)
	}
}

func TestOpenUnknownSuite(t *testing.T) {
	f := newFixture(t)
	fr, _ := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "hi"))
	fr.Header.Suite = "rot13"
	fr = resign(fr, f.alice)
	if _, err := Open(fr, f.alice.Public(), f.bob.MachineID(), f.resolver); !errors.Is(err, ErrUnknownSuite) {
		t.Fatalf("Open = %v, want ErrUnknownSuite", err)
	}
}

// A paired peer (Mallory) signs a header that claims to come from Alice.
func TestOpenLyingHeaderSignedByOtherPeer(t *testing.T) {
	f := newFixture(t)
	fr, _ := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "hi"))
	lying := resign(fr, f.mallory) // header still says from_machine = alice
	if _, err := Open(lying, f.mallory.Public(), f.bob.MachineID(), f.resolver); !errors.Is(err, ErrMismatch) {
		t.Fatalf("Open = %v, want ErrMismatch", err)
	}
}

// Mallory builds an honest-looking outer frame from herself whose sealed
// envelope claims to be from Alice. The inner/outer check must catch it.
func TestOpenInnerOuterMismatch(t *testing.T) {
	f := newFixture(t)
	inner := f.chat(t, f.alice, f.bob, "I am alice, honest")
	pt, _ := json.Marshal(inner)
	h := Header{V: 1, ID: inner.ID, FromMachine: f.mallory.MachineID(), ToMachine: f.bob.MachineID(), PKID: f.bobSigned.ID, Suite: DefaultSuite}
	suite, _ := Lookup(DefaultSuite)
	payload, err := suite.Seal(f.bobSigned.Pub, h.info(), pt)
	if err != nil {
		t.Fatal(err)
	}
	fr := resign(Frame{Header: h, Payload: payload}, f.mallory)
	if _, err := Open(fr, f.mallory.Public(), f.bob.MachineID(), f.resolver); !errors.Is(err, ErrMismatch) {
		t.Fatalf("Open = %v, want ErrMismatch", err)
	}
}

// The sender is legitimate but the header ID differs from the envelope ID.
func TestOpenInnerIDMismatch(t *testing.T) {
	f := newFixture(t)
	inner := f.chat(t, f.alice, f.bob, "hi")
	pt, _ := json.Marshal(inner)
	h := Header{V: 1, ID: core.NewID(), FromMachine: f.alice.MachineID(), ToMachine: f.bob.MachineID(), PKID: f.bobSigned.ID, Suite: DefaultSuite}
	suite, _ := Lookup(DefaultSuite)
	payload, _ := suite.Seal(f.bobSigned.Pub, h.info(), pt)
	fr := resign(Frame{Header: h, Payload: payload}, f.alice)
	if _, err := Open(fr, f.alice.Public(), f.bob.MachineID(), f.resolver); !errors.Is(err, ErrMismatch) {
		t.Fatalf("Open = %v, want ErrMismatch", err)
	}
}

// Signature stripping: Mallory takes Alice's frame, rewrites the header to
// name herself, and re-signs. The header is bound into the HPKE info, so
// decryption fails; the frame must never open.
func TestOpenSignatureStrippingResignRejected(t *testing.T) {
	f := newFixture(t)
	fr, _ := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "from alice"))
	stolen := fr
	stolen.Header.FromMachine = f.mallory.MachineID()
	stolen = resign(stolen, f.mallory)
	env, err := Open(stolen, f.mallory.Public(), f.bob.MachineID(), f.resolver)
	if err == nil {
		t.Fatalf("re-signed frame opened as %+v", env)
	}
	if errors.Is(err, ErrBadSignature) || errors.Is(err, ErrUnknownPrekey) {
		t.Fatalf("expected a decryption or mismatch failure, got %v", err)
	}
}

func flipped(b []byte, i int) []byte {
	c := append([]byte(nil), b...)
	c[i] ^= 0x01
	return c
}

// Only pk_id is switched to another prekey Bob really holds, and the frame
// is re-signed by the real sender. The header is bound into HPKE info and
// the key differs, so decryption must fail.
func TestOpenPKIDSwitchedToOtherRetainedPrekey(t *testing.T) {
	f := newFixture(t)
	fr, err := Seal(f.alice, f.bobSigned, f.chat(t, f.alice, f.bob, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := keys.GeneratePrekey(f.clock.Now().Add(core.PrekeyRotation))
	if err != nil {
		t.Fatal(err)
	}
	f.resolver[other.ID] = other.Priv
	fr.Header.PKID = other.ID
	fr = resign(fr, f.alice)
	env, err := Open(fr, f.alice.Public(), f.bob.MachineID(), f.resolver)
	if err == nil {
		t.Fatalf("frame with switched pk_id opened as %+v", env)
	}
	if errors.Is(err, ErrBadSignature) || errors.Is(err, ErrUnknownPrekey) {
		t.Fatalf("expected a decryption failure, got %v", err)
	}
}

func TestOpenRejectsEnvelopeVersion(t *testing.T) {
	f := newFixture(t)
	for _, v := range []int{0, 2} {
		inner := f.chat(t, f.alice, f.bob, "hi")
		inner.V = v
		pt, _ := json.Marshal(inner)
		h := Header{V: 1, ID: inner.ID, FromMachine: f.alice.MachineID(), ToMachine: f.bob.MachineID(), PKID: f.bobSigned.ID, Suite: DefaultSuite}
		suite, _ := Lookup(DefaultSuite)
		payload, err := suite.Seal(f.bobSigned.Pub, h.info(), pt)
		if err != nil {
			t.Fatal(err)
		}
		fr := resign(Frame{Header: h, Payload: payload}, f.alice)
		if _, err := Open(fr, f.alice.Public(), f.bob.MachineID(), f.resolver); !errors.Is(err, ErrMismatch) {
			t.Fatalf("envelope v=%d: Open = %v, want ErrMismatch", v, err)
		}
	}
}

func TestSealDoesNotHTMLEscape(t *testing.T) {
	f := newFixture(t)
	env := f.chat(t, f.alice, f.bob, strings.Repeat("<", core.MaxTextBytes))
	fr, err := Seal(f.alice, f.bobSigned, env)
	if err != nil {
		t.Fatalf("64 KiB of '<' must seal: %v", err)
	}
	raw, _ := fr.Marshal()
	if len(raw) > core.MaxFrameBytes {
		t.Fatalf("frame is %d bytes", len(raw))
	}
	parsed, err := ParseFrame(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(parsed, f.alice.Public(), f.bob.MachineID(), f.resolver)
	if err != nil {
		t.Fatal(err)
	}
	var body core.ChatBody
	if err := json.Unmarshal(got.Body, &body); err != nil || body.Text != strings.Repeat("<", core.MaxTextBytes) {
		t.Fatalf("body did not round-trip: %v", err)
	}
}

func TestSealTooLarge(t *testing.T) {
	f := newFixture(t)
	// Control characters JSON-escape to \u0001 (6 bytes each): 384 KiB.
	env := f.chat(t, f.alice, f.bob, strings.Repeat("\x01", core.MaxTextBytes))
	if _, err := Seal(f.alice, f.bobSigned, env); !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("Seal = %v, want ErrTooLarge", err)
	}
	if err := FitsFrame(env); !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("FitsFrame = %v, want ErrTooLarge", err)
	}
}

// FitsFrame assumes the longest allowed pk_id, so it is exact for such a
// prekey and conservative for shorter ones.
func TestFitsFrameAgreesWithSealAtBoundary(t *testing.T) {
	f := newFixture(t)
	long := *f.bobPK
	long.ID = strings.Repeat("P", MaxIDLen)
	longSigned := long.Signed(f.bob)

	envOf := func(n int) core.Envelope { return f.chat(t, f.alice, f.bob, strings.Repeat("a", n)) }
	// Largest n that FitsFrame accepts.
	lo, hi := 0, core.MaxFrameBytes
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if FitsFrame(envOf(mid)) == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	n := lo
	if n < core.MaxTextBytes {
		t.Fatalf("boundary %d is below MaxTextBytes", n)
	}
	for _, tc := range []struct {
		n    int
		fits bool
	}{{n, true}, {n + 1, false}} {
		env := envOf(tc.n)
		fitErr := FitsFrame(env)
		if (fitErr == nil) != tc.fits {
			t.Fatalf("n=%d: FitsFrame = %v", tc.n, fitErr)
		}
		fr, sealErr := Seal(f.alice, longSigned, env)
		if tc.fits {
			if sealErr != nil {
				t.Fatalf("n=%d: FitsFrame ok but Seal = %v", tc.n, sealErr)
			}
			raw, _ := fr.Marshal()
			if len(raw) > core.MaxFrameBytes {
				t.Fatalf("n=%d: frame is %d bytes", tc.n, len(raw))
			}
		} else if !errors.Is(sealErr, core.ErrTooLarge) {
			t.Fatalf("n=%d: FitsFrame refused but Seal = %v", tc.n, sealErr)
		}
		// With a normal-length prekey, FitsFrame ok always implies Seal ok.
		if fitErr == nil {
			if _, err := Seal(f.alice, f.bobSigned, env); err != nil {
				t.Fatalf("n=%d: FitsFrame ok but Seal with short pk_id = %v", tc.n, err)
			}
		}
	}
}

func TestSealRejectsMalformedHeaderFields(t *testing.T) {
	f := newFixture(t)
	bad := f.bobSigned
	bad.ID = "has space"
	if _, err := Seal(f.alice, bad, f.chat(t, f.alice, f.bob, "x")); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Seal = %v, want ErrMalformed", err)
	}
}
