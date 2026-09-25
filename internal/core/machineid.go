package core

// MachineID is the lower-case, unpadded RFC 4648 base32 encoding of the
// SHA-256 of a machine's identity public key (52 characters).
type MachineID string

// Short returns the first 16 characters, used for display.
func (m MachineID) Short() string {
	if len(m) <= 16 {
		return string(m)
	}
	return string(m[:16])
}
