package sealing

import (
	"encoding/json"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
)

// FrameVersion is the peer-v1 outer header version.
const FrameVersion = 1

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

// ParseFrame decodes a frame and checks its shape (not its signature).
func ParseFrame(b []byte) (Frame, error) {
	if len(b) > core.MaxFrameBytes {
		return Frame{}, fmt.Errorf("sealing: frame is %d bytes: %w", len(b), core.ErrTooLarge)
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return Frame{}, fmt.Errorf("sealing: parse frame: %w", err)
	}
	h := f.Header
	if h.V != FrameVersion {
		return Frame{}, fmt.Errorf("sealing: unsupported frame version %d", h.V)
	}
	if h.ID == "" || h.FromMachine == "" || h.ToMachine == "" || h.PKID == "" || h.Suite == "" {
		return Frame{}, fmt.Errorf("sealing: frame header is missing fields")
	}
	if len(f.Payload) == 0 || len(f.Sig) == 0 {
		return Frame{}, fmt.Errorf("sealing: frame has no payload or signature")
	}
	return f, nil
}
