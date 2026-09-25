package sealing

import (
	"crypto/ecdh"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
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
	id := "test-registry-" + core.NewID()
	if _, ok := Lookup(id); ok {
		t.Fatal("unexpected suite before Register")
	}
	Register(fakeSuite{id: id})
	t.Cleanup(func() { unregister(id) })
	s, ok := Lookup(id)
	if !ok || s.ID() != id {
		t.Fatalf("Lookup = %v, %v", s, ok)
	}
	if _, ok := Lookup(DefaultSuite); !ok {
		t.Fatal("DefaultSuite must be registered by init")
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "sealing: duplicate suite "+DefaultSuite) {
			t.Fatalf("recover() = %v, want duplicate suite panic", r)
		}
	}()
	Register(fakeSuite{id: DefaultSuite})
}

func TestUnregister(t *testing.T) {
	id := "test-unregister-" + core.NewID()
	Register(fakeSuite{id: id})
	unregister(id)
	if _, ok := Lookup(id); ok {
		t.Fatal("suite still registered after unregister")
	}
	Register(fakeSuite{id: id}) // re-registering after unregister is allowed
	unregister(id)
}
