//go:build !cgo || !(linux || darwin)

package auth

// NewPAM returns a Verifier that always fails with ErrUnavailable, because
// this binary was built without cgo (or for an OS without PAM support).
// A service that is not allowlisted fails with the allowlist error instead,
// which also matches ErrUnavailable.
func NewPAM(service string) Verifier {
	if err := CheckPAMService(service); err != nil {
		return refused{err: err}
	}
	return unavailable{}
}
