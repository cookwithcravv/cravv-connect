package pake

import (
	"bytes"
	"errors"
	"testing"
)

func run(t *testing.T, pwA, pwB string) (keyA, keyB []byte) {
	t.Helper()
	var f Factory = SPAKE2{}
	a, err := f.New([]byte(pwA), SideA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.New([]byte(pwB), SideB)
	if err != nil {
		t.Fatal(err)
	}
	if keyA, err = a.Finish(b.Message()); err != nil {
		t.Fatal(err)
	}
	if keyB, err = b.Finish(a.Message()); err != nil {
		t.Fatal(err)
	}
	return keyA, keyB
}

func TestSPAKE2Agree(t *testing.T) {
	ka, kb := run(t, "9QXMTR2A", "9QXMTR2A")
	if len(ka) != 32 || !bytes.Equal(ka, kb) {
		t.Fatalf("keys differ: %x vs %x", ka, kb)
	}
	// Both sides confirm each other.
	if !CheckConfirm(kb, SideA, ConfirmTag(ka, SideA)) || !CheckConfirm(ka, SideB, ConfirmTag(kb, SideB)) {
		t.Fatal("confirmation failed with equal keys")
	}
	if !bytes.Equal(SessionKey(ka), SessionKey(kb)) || len(SessionKey(ka)) != 32 {
		t.Fatal("session keys differ")
	}
	if bytes.Equal(SessionKey(ka), ka) {
		t.Fatal("session key must be derived, not the raw PAKE key")
	}
}

func TestSPAKE2FreshKeysEachRun(t *testing.T) {
	k1, _ := run(t, "PW", "PW")
	k2, _ := run(t, "PW", "PW")
	if bytes.Equal(k1, k2) {
		t.Fatal("two runs produced the same key")
	}
}

func TestSPAKE2WrongPassword(t *testing.T) {
	ka, kb := run(t, "9QXMTR2A", "9QXMTR2B")
	if bytes.Equal(ka, kb) {
		t.Fatal("different passwords produced the same key")
	}
	if CheckConfirm(kb, SideA, ConfirmTag(ka, SideA)) {
		t.Fatal("B accepted A's confirmation under a wrong password")
	}
	if CheckConfirm(ka, SideB, ConfirmTag(kb, SideB)) {
		t.Fatal("A accepted B's confirmation under a wrong password")
	}
}

func TestConfirmTagsAreSideSpecific(t *testing.T) {
	ka, _ := run(t, "PW", "PW")
	if bytes.Equal(ConfirmTag(ka, SideA), ConfirmTag(ka, SideB)) {
		t.Fatal("A and B tags must differ (no reflection)")
	}
	if CheckConfirm(ka, SideB, ConfirmTag(ka, SideA)) {
		t.Fatal("A's own tag reflected back must not pass as B's")
	}
	if CheckConfirm(ka, SideA, nil) {
		t.Fatal("empty tag accepted")
	}
}

func TestFinishRejectsBadMessages(t *testing.T) {
	var f Factory = SPAKE2{}
	peerA, _ := f.New([]byte("pw"), SideA)
	fromA := peerA.Message()
	notAPoint := append([]byte{'A'}, bytes.Repeat([]byte{0xFF}, 32)...)
	tests := []struct {
		name string
		side Side // side that calls Finish
		msg  []byte
	}{
		{"empty", SideB, nil},
		{"short", SideB, fromA[:10]},
		{"long", SideB, append(append([]byte(nil), fromA...), 0)},
		{"same side (A to A)", SideA, fromA},
		{"symmetric side byte", SideB, append([]byte{'S'}, fromA[1:]...)},
		{"not a curve point", SideB, notAPoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, err := f.New([]byte("pw"), tt.side)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := x.Finish(tt.msg); !errors.Is(err, ErrBadMessage) {
				t.Fatalf("Finish = %v, want ErrBadMessage", err)
			}
		})
	}
}

func TestFinishReflectedMessage(t *testing.T) {
	b, _ := SPAKE2{}.New([]byte("pw"), SideB)
	own := b.Message()
	own[0] = 'A' // pretend our own element came from A
	if _, err := b.Finish(own); !errors.Is(err, ErrBadMessage) {
		t.Fatalf("reflected message: %v, want ErrBadMessage", err)
	}
}

func TestFinishOnlyOnce(t *testing.T) {
	a, _ := SPAKE2{}.New([]byte("pw"), SideA)
	b, _ := SPAKE2{}.New([]byte("pw"), SideB)
	if _, err := a.Finish(b.Message()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Finish(b.Message()); err == nil {
		t.Fatal("second Finish succeeded")
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := (SPAKE2{}).New([]byte("pw"), Side(3)); err == nil {
		t.Fatal("invalid side accepted")
	}
	if _, err := (SPAKE2{}).New(nil, SideA); err == nil {
		t.Fatal("empty password accepted")
	}
	a, _ := SPAKE2{}.New([]byte("pw"), SideA)
	m := a.Message()
	if len(m) != 33 || m[0] != 'A' {
		t.Fatalf("message = %x", m)
	}
	m[1] ^= 1
	if bytes.Equal(m, a.Message()) {
		t.Fatal("Message() must return a copy")
	}
}
