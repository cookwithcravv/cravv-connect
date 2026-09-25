package pathguard

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
)

// MaxNameLen is the longest sanitized file name.
const MaxNameLen = 100

// SanitizeName turns a peer-chosen file name into a safe basename: the part
// after the last / or \, every byte outside [A-Za-z0-9._-] replaced by '_'
// (one '_' per rune), leading dots removed, at most MaxNameLen bytes with a
// short extension kept, and "file" when nothing is left.
func SanitizeName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.TrimLeft(b.String(), ".")
	if len(s) > MaxNameLen {
		ext := filepath.Ext(s)
		if len(ext) > 16 {
			ext = ""
		}
		s = s[:MaxNameLen-len(ext)] + ext
	}
	if s == "" {
		return "file"
	}
	return s
}

// InboundPath returns filesDir/alias/msgID-SanitizeName(name) and verifies
// the result stays inside filesDir. alias is the local alias and msgID the
// message ID; both must be plain path components. Errors wrap core.ErrPathRefused.
func InboundPath(filesDir, alias, msgID, name string) (string, error) {
	if !plainComponent(alias) {
		return "", fmt.Errorf("%w: bad alias %q for a files folder", core.ErrPathRefused, alias)
	}
	if !plainComponent(msgID) || strings.Contains(msgID, ".") {
		return "", fmt.Errorf("%w: bad message id %q", core.ErrPathRefused, msgID)
	}
	root, err := filepath.Abs(filesDir)
	if err != nil {
		return "", fmt.Errorf("%w: %v", core.ErrPathRefused, err)
	}
	p := filepath.Join(root, alias, msgID+"-"+SanitizeName(name))
	rel, ok := within(root, p)
	if !ok || rel == "." {
		return "", fmt.Errorf("%w: %s escapes the files folder", core.ErrPathRefused, p)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("%w: %s would be hidden", core.ErrPathRefused, p)
		}
	}
	return p, nil
}

// plainComponent: 1 to 64 bytes of [A-Za-z0-9._-], not starting with '.'.
func plainComponent(s string) bool {
	if s == "" || len(s) > 64 || s[0] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
