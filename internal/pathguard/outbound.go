// Package pathguard decides which local files may be sent to a peer and
// where received files may be written.
package pathguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
)

// Outbound enforces the outbound file rules: only regular files inside the
// session's project folder or a human-approved extra root, never under a
// dot-directory, never a known secret file, never larger than core.MaxFileBytes.
type Outbound struct {
	extraRoots []string
}

func NewOutbound(extraRoots []string) *Outbound {
	return &Outbound{extraRoots: append([]string(nil), extraRoots...)}
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", core.ErrPathRefused, fmt.Sprintf(format, args...))
}

// Check returns the cleaned absolute real path of a regular file inside
// projectDir or an extra root. A relative path is taken relative to
// projectDir. Symlinks are resolved and the result is checked again, so a
// link cannot lead outside the roots or into a dot-directory. Every refusal
// wraps core.ErrPathRefused.
func (o *Outbound) Check(projectDir, path string) (string, os.FileInfo, error) {
	if path == "" {
		return "", nil, refuse("empty path")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(projectDir, path)
	}
	lexical := filepath.Clean(path)

	type root struct{ lexical, real string }
	var roots []root
	for _, r := range append([]string{projectDir}, o.extraRoots...) {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue
		}
		roots = append(roots, root{lexical: abs, real: real})
	}
	if len(roots) == 0 {
		return "", nil, refuse("no project folder registered")
	}

	// The path as written must not name a dot-directory or secret file,
	// even if a symlink would lead somewhere harmless.
	for _, r := range roots {
		if rel, ok := within(r.lexical, lexical); ok {
			if err := checkComponents(rel); err != nil {
				return "", nil, err
			}
			break
		}
	}

	real, err := filepath.EvalSymlinks(lexical)
	if err != nil {
		return "", nil, refuse("%s cannot be resolved", lexical)
	}
	inside := false
	for _, r := range roots {
		if rel, ok := within(r.real, real); ok {
			if err := checkComponents(rel); err != nil {
				return "", nil, err
			}
			inside = true
			break
		}
	}
	if !inside {
		return "", nil, refuse("%s is outside the project folder and allowed paths", lexical)
	}

	fi, err := os.Stat(real)
	if err != nil {
		return "", nil, refuse("%s cannot be read", lexical)
	}
	if !fi.Mode().IsRegular() {
		return "", nil, refuse("%s is not a regular file", lexical)
	}
	if fi.Size() > core.MaxFileBytes {
		return "", nil, refuse("%s is larger than 100 MB", lexical)
	}
	return real, fi, nil
}

// within reports whether target is root itself or below it, and returns the
// relative path.
func within(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// checkComponents refuses any component starting with "." and a final
// name that matches a known secret pattern.
func checkComponents(rel string) error {
	if rel == "." {
		return refuse("a folder cannot be sent")
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, p := range parts {
		if strings.HasPrefix(p, ".") {
			return refuse("%s is a hidden file or inside a hidden folder", rel)
		}
	}
	if isSecretName(parts[len(parts)-1]) {
		return refuse("%s looks like a secret (key, certificate, or env file)", rel)
	}
	return nil
}

// secretSuffixes are file extensions of keys, certificates, keystores,
// password databases, VPN profiles and env files.
var secretSuffixes = []string{
	".pem", ".key", ".env",
	".p12", ".pfx", ".jks", ".keystore",
	".kdbx", ".ppk", ".ovpn",
}

// isSecretName matches (case-insensitive) .env*, id_*, credentials*.json,
// service-account*.json and names ending in one of secretSuffixes.
// Dotfiles such as .netrc, .npmrc and .pypirc are refused earlier as
// hidden files.
func isSecretName(name string) bool {
	n := strings.ToLower(name)
	if strings.HasPrefix(n, ".env") || strings.HasPrefix(n, "id_") {
		return true
	}
	if strings.HasSuffix(n, ".json") &&
		(strings.HasPrefix(n, "credentials") || strings.HasPrefix(n, "service-account")) {
		return true
	}
	for _, suf := range secretSuffixes {
		if strings.HasSuffix(n, suf) {
			return true
		}
	}
	return false
}
