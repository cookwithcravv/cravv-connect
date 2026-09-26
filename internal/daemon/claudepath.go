package daemon

import (
	"os"
	"path/filepath"
)

// EnvClaude names the claude executable managed runs use. Without it the
// daemon looks on its PATH and then in the usual install places, because a
// daemon started by launchd or systemd has a short PATH.
const EnvClaude = "CRAVV_CLAUDE"

// FindClaude returns the claude executable to run: $CRAVV_CLAUDE, claude on
// the PATH, the first executable of the usual install places, or "claude"
// (the run then fails to start and its task says so).
func FindClaude(getenv func(string) string, lookPath func(string) (string, error), home string) string {
	if p := getenv(EnvClaude); p != "" {
		return p
	}
	if p, err := lookPath("claude"); err == nil {
		return p
	}
	var places []string
	if home != "" {
		places = append(places, filepath.Join(home, ".local", "bin", "claude"), filepath.Join(home, ".claude", "local", "claude"))
	}
	places = append(places, "/opt/homebrew/bin/claude", "/usr/local/bin/claude")
	for _, p := range places {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return "claude"
}
