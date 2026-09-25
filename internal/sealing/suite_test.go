package sealing

import (
	"crypto/ecdh"
	"testing"
)

type fakeSuite struct{ id string }

func (f fakeSuite) ID() string { return f.id }
func (fakeSuite) Seal(_ []byte, _, pt []byte) ([]byte, error) {
	return append([]byte(nil), pt...), nil
}
func (fakeSuite) Open(_ *ecdh.PrivateKey, _, payload []byte) ([]byte, error) {
	return append([]byte(nil), payload...), nil
}

func TestRegistryRegisterLookup(t *testing.T) {
	if _, ok := Lookup("test-registry-suite"); ok {
		t.Fatal("unexpected suite before Register")
	}
	Register(fakeSuite{id: "test-registry-suite"})
	s, ok := Lookup("test-registry-suite")
	if !ok || s.ID() != "test-registry-suite" {
		t.Fatalf("Lookup = %v, %v", s, ok)
	}
	if _, ok := Lookup(DefaultSuite); !ok {
		t.Fatal("DefaultSuite must be registered by init")
	}
}
