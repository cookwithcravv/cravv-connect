// Package pake runs the password-authenticated key exchange used for pairing.
package pake

// Side is the role in the exchange. The pair creator is A, the joiner is B.
type Side int

const (
	SideA Side = 1
	SideB Side = 2
)

// Valid reports whether s is SideA or SideB.
func (s Side) Valid() bool { return s == SideA || s == SideB }

// Other returns the opposite side.
func (s Side) Other() Side {
	if s == SideA {
		return SideB
	}
	return SideA
}

// Exchange is one side of a single-message-each PAKE.
type Exchange interface {
	Message() []byte                       // first (only) outbound message
	Finish(peerMsg []byte) ([]byte, error) // 32-byte shared key
}

// Factory starts exchanges. Other PAKEs can be added by implementing it.
type Factory interface {
	New(password []byte, side Side) (Exchange, error)
}
