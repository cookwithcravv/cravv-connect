package keys

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// ErrBadPrekeySignature is returned when a signed prekey does not verify.
var ErrBadPrekeySignature = errors.New("keys: bad prekey signature")

// Prekey is an X25519 key pair used to receive sealed messages.
type Prekey struct {
	ID        string
	Priv      *ecdh.PrivateKey // X25519
	CreatedAt time.Time
}

// GeneratePrekey creates a new random prekey with a fresh core.NewID.
func GeneratePrekey(now time.Time) (*Prekey, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("keys: generate prekey: %w", err)
	}
	return &Prekey{ID: core.NewID(), Priv: priv, CreatedAt: now}, nil
}

// PrekeyFromBytes rebuilds a stored prekey from its 32-byte private key.
func PrekeyFromBytes(id string, priv []byte, createdAt time.Time) (*Prekey, error) {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("keys: load prekey %s: %w", id, err)
	}
	return &Prekey{ID: id, Priv: k, CreatedAt: createdAt}, nil
}

// Signed returns the public half signed by the identity.
func (p *Prekey) Signed(id *Identity) SignedPrekey {
	s := SignedPrekey{
		ID:        p.ID,
		Pub:       p.Priv.PublicKey().Bytes(),
		CreatedAt: p.CreatedAt.UnixMilli(),
	}
	s.Sig = id.Sign(s.signingBytes())
	return s
}

// SignedPrekey is the public prekey record sent to peers.
type SignedPrekey struct {
	ID        string `json:"id"`
	Pub       []byte `json:"pub"`
	CreatedAt int64  `json:"created_at"` // unix ms
	Sig       []byte `json:"sig"`
}

// signingBytes is "cravv-prekey-v1\n" + ID + "\n" + base64(Pub) + "\n" + decimal(CreatedAt).
func (s SignedPrekey) signingBytes() []byte {
	msg := "cravv-prekey-v1\n" + s.ID + "\n" + base64.StdEncoding.EncodeToString(s.Pub) + "\n" + strconv.FormatInt(s.CreatedAt, 10)
	return []byte(msg)
}

// Verify checks the signature against the owner's identity key and that Pub
// is a valid X25519 public key.
func (s SignedPrekey) Verify(ik ed25519.PublicKey) error {
	if _, err := ecdh.X25519().NewPublicKey(s.Pub); err != nil {
		return fmt.Errorf("%w: invalid public key", ErrBadPrekeySignature)
	}
	if s.ID == "" || !Verify(ik, s.signingBytes(), s.Sig) {
		return ErrBadPrekeySignature
	}
	return nil
}

// Wire converts to the core mirror type used in envelope bodies.
func (s SignedPrekey) Wire() core.SignedPrekeyWire {
	return core.SignedPrekeyWire{ID: s.ID, Pub: s.Pub, CreatedAt: s.CreatedAt, Sig: s.Sig}
}

// SignedPrekeyFromWire converts back from the core mirror type.
func SignedPrekeyFromWire(w core.SignedPrekeyWire) SignedPrekey {
	return SignedPrekey{ID: w.ID, Pub: w.Pub, CreatedAt: w.CreatedAt, Sig: w.Sig}
}
