package childenv

import (
	"slices"
	"testing"
)

func TestFilterKeepsOnlyTheAllowlist(t *testing.T) {
	in := []string{
		"HOME=/home/u", "PATH=/usr/bin", "USER=u", "LOGNAME=u", "SHELL=/bin/zsh", "LANG=en_US.UTF-8",
		"LC_ALL=C", "LC_CTYPE=UTF-8", "TMPDIR=/tmp/u", "TERM=xterm",
		"ANTHROPIC_API_KEY=sk-1", "CLAUDE_CODE_OAUTH_TOKEN=oat", "ANTHROPIC_BASE_URL=https://x", "CLAUDE_CONFIG_DIR=/c",
		"HTTPS_PROXY=http://p", "no_proxy=localhost",
		"CRAVV_HOME=/state", "CRAVV_RUN_TOKEN=tok", "CRAVV_ADMIN_TOKEN=adm", "CRAVV_ANYTHING=1",
		"AWS_SECRET_ACCESS_KEY=s", "GITHUB_TOKEN=g", "SSH_AUTH_SOCK=/s", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=x",
		"LCX=no", "malformed",
	}
	got := Filter(in)
	want := []string{
		"HOME=/home/u", "PATH=/usr/bin", "USER=u", "LOGNAME=u", "SHELL=/bin/zsh", "LANG=en_US.UTF-8",
		"LC_ALL=C", "LC_CTYPE=UTF-8", "TMPDIR=/tmp/u", "TERM=xterm",
		"ANTHROPIC_API_KEY=sk-1", "CLAUDE_CODE_OAUTH_TOKEN=oat", "ANTHROPIC_BASE_URL=https://x", "CLAUDE_CONFIG_DIR=/c",
		"HTTPS_PROXY=http://p", "no_proxy=localhost",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Filter:\n got %q\nwant %q", got, want)
	}
}

func TestAllowAddsNamesButNeverTheRunToken(t *testing.T) {
	t.Cleanup(func() { mu.Lock(); extra = map[string]bool{}; mu.Unlock() })
	Allow("CRAVV_FAKE_X", "CRAVV_RUN_TOKEN")
	got := Filter([]string{"CRAVV_FAKE_X=1", "CRAVV_RUN_TOKEN=tok", "CRAVV_HOME=/s"})
	if !slices.Equal(got, []string{"CRAVV_FAKE_X=1"}) {
		t.Fatalf("Filter = %q", got)
	}
}
