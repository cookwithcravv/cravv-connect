//go:build !cgo || !(linux || darwin)

package auth

// NewPAM returns a Verifier that always fails with ErrUnavailable, because
// this binary was built without cgo (or for an OS without PAM support).
func NewPAM(service string) Verifier { return unavailable{} }
