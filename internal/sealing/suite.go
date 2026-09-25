// Package sealing seals envelopes into signed, encrypted frames (peer-v1).
package sealing

import (
	"crypto/ecdh"
	"sync"
)

// Suite is one public-key encryption scheme. New suites register themselves
// without any change to Seal or Open.
type Suite interface {
	ID() string
	Seal(recipientPub []byte, info, plaintext []byte) ([]byte, error)
	Open(recipientPriv *ecdh.PrivateKey, info, payload []byte) ([]byte, error)
}

var (
	suitesMu sync.RWMutex
	suites   = map[string]Suite{}
)

// Register adds a suite. It panics if a suite with the same ID is already
// registered, so one suite can never silently replace another.
func Register(s Suite) {
	suitesMu.Lock()
	defer suitesMu.Unlock()
	if _, dup := suites[s.ID()]; dup {
		panic("sealing: duplicate suite " + s.ID())
	}
	suites[s.ID()] = s
}

// unregister removes a suite. It exists for tests.
func unregister(id string) {
	suitesMu.Lock()
	defer suitesMu.Unlock()
	delete(suites, id)
}

// Lookup returns the suite with the given ID.
func Lookup(id string) (Suite, bool) {
	suitesMu.RLock()
	defer suitesMu.RUnlock()
	s, ok := suites[id]
	return s, ok
}
