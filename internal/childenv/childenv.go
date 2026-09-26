// Package childenv decides which of the daemon's environment variables
// reach a managed run's agent process. Only an allowlist does: what a
// process needs to find its home, tools, locale and terminal, and what
// Claude Code needs to authenticate and reach the API. Nothing named
// CRAVV_* passes (the daemon's own settings and secrets), and the run token
// never does.
package childenv

import (
	"strings"
	"sync"
)

// allowed are the names that pass.
//
// Claude Code authenticates with the macOS keychain or ~/.claude (found by
// HOME and USER), or with ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN or
// CLAUDE_CODE_OAUTH_TOKEN when the daemon has them; ANTHROPIC_BASE_URL and
// CLAUDE_CONFIG_DIR point it at another endpoint or config folder, and the
// proxy variables let it reach the API through a proxy.
var allowed = map[string]bool{
	"HOME": true, "PATH": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"LANG": true, "TMPDIR": true, "TERM": true,
	"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true, "ANTHROPIC_BASE_URL": true,
	"CLAUDE_CODE_OAUTH_TOKEN": true, "CLAUDE_CONFIG_DIR": true,
	"HTTPS_PROXY": true, "HTTP_PROXY": true, "NO_PROXY": true,
	"https_proxy": true, "http_proxy": true, "no_proxy": true,
}

// runToken is never passed, even if something allowed it.
const runToken = "CRAVV_RUN_TOKEN"

var (
	mu    sync.Mutex
	extra = map[string]bool{}
)

// Allow lets more names pass. Only test fakes call it (the fake agent
// reads its mode from the environment); the daemon never does.
func Allow(names ...string) {
	mu.Lock()
	defer mu.Unlock()
	for _, n := range names {
		extra[n] = true
	}
}

// Filter returns the KEY=VALUE pairs of env whose names pass, in order:
// the allowlist, LC_* locale variables, and names added with Allow.
func Filter(env []string) []string {
	mu.Lock()
	defer mu.Unlock()
	var out []string
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == runToken {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "LC_") || extra[name] {
			out = append(out, kv)
		}
	}
	return out
}
