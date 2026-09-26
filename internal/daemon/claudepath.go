package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnvClaude names the claude executable managed runs use. Without it the
// daemon looks on its PATH and then in the usual install places, because a
// daemon started by launchd or systemd has a short PATH.
const EnvClaude = "CRAVV_CLAUDE"

// ErrClaudeNotFound refuses an offer the daemon could not run.
var ErrClaudeNotFound = errors.New("claude not found by the daemon: set CRAVV_CLAUDE in the daemon's service environment")

// FindClaude returns the claude executable to run: $CRAVV_CLAUDE, claude on
// the PATH, the first executable of the usual install places, or "claude"
// (the run then fails to start and its task says so).
func FindClaude(getenv func(string) string, lookPath func(string) (string, error), home string) string {
	p, _ := ResolveClaude(getenv, lookPath, home)
	return p
}

// ResolveClaude is FindClaude that also reports whether the executable it
// returns exists: $CRAVV_CLAUDE (a path, or a name looked up on the PATH),
// claude on the PATH, or one of the usual install places.
func ResolveClaude(getenv func(string) string, lookPath func(string) (string, error), home string) (string, bool) {
	if p := getenv(EnvClaude); p != "" {
		if !strings.ContainsRune(p, filepath.Separator) {
			if found, err := lookPath(p); err == nil {
				return found, true
			}
			return p, false
		}
		return p, executable(p)
	}
	if p, err := lookPath("claude"); err == nil {
		return p, true
	}
	var places []string
	if home != "" {
		places = append(places, filepath.Join(home, ".local", "bin", "claude"), filepath.Join(home, ".claude", "local", "claude"))
	}
	places = append(places, "/opt/homebrew/bin/claude", "/usr/local/bin/claude")
	for _, p := range places {
		if executable(p) {
			return p, true
		}
	}
	return "claude", false
}

func executable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}
