package daemon

import (
	"errors"
	"strings"
)

// MaxAliasLen is the longest local alias (spec 6.2).
const MaxAliasLen = 24

// ErrBadAlias is returned when an alias has no usable characters.
var ErrBadAlias = errors.New("invalid alias: use a-z, 0-9 and -")

// SanitizeAlias lowercases s, maps every character outside [a-z0-9-] to '-',
// collapses runs of '-', trims '-' at both ends and caps the result at 24 characters.
// It returns "" when nothing usable remains.
func SanitizeAlias(s string) string {
	var b strings.Builder
	lastDash := true // suppresses leading dashes
	for _, r := range strings.ToLower(s) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := b.String()
	if len(out) > MaxAliasLen {
		out = out[:MaxAliasLen]
	}
	return strings.Trim(out, "-")
}
