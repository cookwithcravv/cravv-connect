package sealing

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
)

// FrameVersion is the peer-v1 outer header version.
const FrameVersion = 1

// MaxIDLen bounds the header's ID, PKID, and Suite fields.
const MaxIDLen = 64

// machineIDLen is the length of a core.MachineID (base32 of SHA-256).
const machineIDLen = 52

// ErrMalformed is returned by ParseFrame (and Seal) when the header has a
// wrong version, a missing field, or a field outside its allowed charset or
// length: ID and PKID are 1..64 of [0-9A-Za-z]; machine IDs are exactly 52
// of [a-z2-7]; Suite is 1..64 printable ASCII (0x20..0x7E).
var ErrMalformed = errors.New("malformed frame header")

// Header is the outer, signed frame header. The relay never parses it.
type Header struct {
	V           int            `json:"v"`
	ID          string         `json:"id"`
	FromMachine core.MachineID `json:"from_machine"`
	ToMachine   core.MachineID `json:"to_machine"`
	PKID        string         `json:"pk_id"`
	Suite       string         `json:"suite"`
}

// Frame is what travels through the relay as opaque bytes.
type Frame struct {
	Header  Header `json:"h"`
	Payload []byte `json:"p"` // suite output
	Sig     []byte `json:"s"` // Ed25519 over signingBytes(header, payload)
}

// canonical returns the header's JSON. Field order is fixed by the struct,
// so both sides produce identical bytes.
func (h Header) canonical() []byte {
	b, err := json.Marshal(h)
	if err != nil {
		// Header has only strings and an int; Marshal cannot fail.
		panic("sealing: marshal header: " + err.Error())
	}
	return b
}

// info is the HPKE info string: it binds the ciphertext to this exact header.
func (h Header) info() []byte {
	return append([]byte("cravv-connect/peer-v1\n"), h.canonical()...)
}

// signingBytes is "cravv-frame-v1\n" + canonical header JSON + "\n" + payload.
func signingBytes(h Header, payload []byte) []byte {
	hb := h.canonical()
	out := make([]byte, 0, len("cravv-frame-v1\n")+len(hb)+1+len(payload))
	out = append(out, "cravv-frame-v1\n"...)
	out = append(out, hb...)
	out = append(out, '\n')
	return append(out, payload...)
}

// Marshal encodes the frame as JSON.
func (f Frame) Marshal() ([]byte, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("sealing: marshal frame: %w", err)
	}
	return b, nil
}

// validate checks the header's version and field syntax.
func (h Header) validate() error {
	switch {
	case h.V != FrameVersion:
		return fmt.Errorf("sealing: unsupported frame version %d: %w", h.V, ErrMalformed)
	case !validToken(h.ID):
		return fmt.Errorf("sealing: bad id %q: %w", h.ID, ErrMalformed)
	case !validToken(h.PKID):
		return fmt.Errorf("sealing: bad pk_id %q: %w", h.PKID, ErrMalformed)
	case !validMachineID(h.FromMachine):
		return fmt.Errorf("sealing: bad from_machine %q: %w", h.FromMachine, ErrMalformed)
	case !validMachineID(h.ToMachine):
		return fmt.Errorf("sealing: bad to_machine %q: %w", h.ToMachine, ErrMalformed)
	case !validSuite(h.Suite):
		return fmt.Errorf("sealing: bad suite %q: %w", h.Suite, ErrMalformed)
	}
	return nil
}

func validToken(s string) bool {
	if len(s) == 0 || len(s) > MaxIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z') {
			return false
		}
	}
	return true
}

func validMachineID(m core.MachineID) bool {
	if len(m) != machineIDLen {
		return false
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		if !('a' <= c && c <= 'z' || '2' <= c && c <= '7') {
			return false
		}
	}
	return true
}

func validSuite(s string) bool {
	if len(s) == 0 || len(s) > MaxIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// ParseFrame decodes a frame and checks its shape (not its signature).
// Header problems are reported as ErrMalformed.
func ParseFrame(b []byte) (Frame, error) {
	if len(b) > core.MaxFrameBytes {
		return Frame{}, fmt.Errorf("sealing: frame is %d bytes: %w", len(b), core.ErrTooLarge)
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return Frame{}, fmt.Errorf("sealing: parse frame: %w", err)
	}
	if err := f.Header.validate(); err != nil {
		return Frame{}, err
	}
	if len(f.Payload) == 0 || len(f.Sig) == 0 {
		return Frame{}, fmt.Errorf("sealing: frame has no payload or signature")
	}
	return f, nil
}
