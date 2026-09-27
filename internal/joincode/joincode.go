// Package joincode builds and parses join codes: a bind code together with
// the relay it was made on, so a new machine needs nothing else to join.
//
//	cravv-join:<relay origin in base32, lower case, no padding>:<nameplate>-<secret>
//
// The relay origin is not secret (the relay sees it on every request); the
// secret half of the bind code is, exactly as in a plain bind code.
package joincode

import (
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// Prefix starts every join code.
const Prefix = "cravv-join"

// MaxLen bounds the input Parse looks at.
const MaxLen = 512

// ErrInvalid is returned for any string that is not a join code.
var ErrInvalid = errors.New("invalid join code (expected cravv-join:<relay>:<code>)")

// ErrOtherRelay is returned by BindCode for a join code made on a relay other
// than this machine's.
var ErrOtherRelay = errors.New("the join code is for another relay")

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Code is a parsed join code.
type Code struct {
	Relay string // normalized relay origin (relayproto.NormalizeOrigin)
	Bind  bindcode.Code
}

// New returns the join code for a bind code made on relay.
func New(relay string, bind bindcode.Code) (Code, error) {
	origin, err := relayproto.NormalizeOrigin(relay)
	if err != nil {
		return Code{}, fmt.Errorf("joincode: %w", err)
	}
	if len(bind.Nameplate) != bindcode.NameplateLen || len(bind.Secret) != bindcode.SecretLen {
		return Code{}, errors.New("joincode: invalid bind code")
	}
	return Code{Relay: origin, Bind: bind}, nil
}

// String renders "cravv-join:<base32 relay>:<nameplate>-<secret>".
func (c Code) String() string {
	return Prefix + ":" + strings.ToLower(b32.EncodeToString([]byte(c.Relay))) + ":" + c.Bind.Nameplate + "-" + c.Bind.Secret
}

// compact drops all white space, so codes broken across lines still parse.
func compact(s string) string { return strings.Join(strings.Fields(s), "") }

// Is reports whether s is meant as a join code rather than a plain bind code
// (it starts with "cravv-join:" in any case, white space ignored).
func Is(s string) bool {
	return strings.HasPrefix(strings.ToLower(compact(s)), Prefix+":")
}

// Parse accepts a join code in any case, with white space anywhere, and the
// bind code part with or without hyphens (bindcode.Parse rules).
func Parse(s string) (Code, error) {
	if len(s) > MaxLen {
		return Code{}, ErrInvalid
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return Code{}, ErrInvalid
		}
	}
	parts := strings.Split(compact(s), ":")
	if len(parts) != 3 || !strings.EqualFold(parts[0], Prefix) {
		return Code{}, ErrInvalid
	}
	raw, err := b32.DecodeString(strings.ToUpper(parts[1]))
	if err != nil {
		return Code{}, ErrInvalid
	}
	// The relay is shown to the person before they join it: refuse anything
	// but printable ASCII, so a lookalike or invisible character cannot pass
	// for another relay (a non-ASCII host must be in punycode).
	if !relayproto.PrintableASCII(string(raw)) {
		return Code{}, fmt.Errorf("%w: the relay in it is not plain ASCII (non-ASCII hosts must be in punycode)", ErrInvalid)
	}
	origin, err := relayproto.NormalizeOrigin(string(raw))
	if err != nil {
		return Code{}, fmt.Errorf("%w: the relay in it is not an http(s) origin", ErrInvalid)
	}
	bind, err := bindcode.Parse("CRAVV" + parts[2])
	if err != nil {
		return Code{}, ErrInvalid
	}
	return Code{Relay: origin, Bind: bind}, nil
}

// BindCode returns what to join with for s: a plain bind code unchanged (the
// daemon parses it), or the bind code inside a join code made on relay, the
// relay this machine uses. A join code for any other relay is refused with
// ErrOtherRelay.
func BindCode(s, relay string) (string, error) {
	if !Is(s) {
		return s, nil
	}
	c, err := Parse(s)
	if err != nil {
		return "", err
	}
	mine, err := relayproto.NormalizeOrigin(relay)
	if err != nil {
		mine = "no relay"
	}
	if mine != c.Relay {
		return "", fmt.Errorf("%w: it is for %s, but this machine uses %s", ErrOtherRelay, c.Relay, mine)
	}
	return c.Bind.String(), nil
}
