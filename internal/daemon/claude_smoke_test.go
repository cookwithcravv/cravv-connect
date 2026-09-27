//go:build darwin || linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/childenv"
	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// TestClaudeSmoke runs the real claude twice with the flags a read-only
// managed run uses: first with --session-id, then --resume of the same
// conversation. It costs two one-line prompts, so it only runs with
// CRAVV_CLAUDE_TEST=1. The MCP server in its config exits at once, which
// claude reports and carries on without.
func TestClaudeSmoke(t *testing.T) {
	if os.Getenv("CRAVV_CLAUDE_TEST") != "1" {
		t.Skip("set CRAVV_CLAUDE_TEST=1 to run the real claude (two one-line prompts)")
	}
	home, _ := os.UserHomeDir()
	adapter := ClaudeAdapter{Path: FindClaude(os.Getenv, exec.LookPath, home)}
	dir := t.TempDir()
	cfg, err := adapter.MCPConfig("/usr/bin/true", map[string]string{EnvRunToken: "smoke"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeTranscripts(t, home, dir) })
	uuid := newUUID()
	for i, resume := range []bool{false, true} {
		cmd := adapter.Command(RunSpec{Folder: dir, AgentSession: uuid, Resume: resume, RunMode: core.RunReadOnly, MCPConfig: path},
			"Reply with the single word ok and nothing else.")
		out := ExecRunner{}.Run(context.Background(), cmd, append(childenv.Filter(os.Environ()), cmd.Env...), 3*time.Minute)
		res := adapter.Result(out.Stdout)
		t.Logf("run %d (resume %v): exit %d in %s, result %+v", i+1, resume, out.ExitCode, out.Duration.Round(time.Millisecond), res)
		if out.Err != nil || out.ExitCode != 0 || !res.Parsed || res.IsError || res.SessionID != uuid {
			t.Fatalf("run %d: %+v\nstdout %s\nstderr %s", i+1, res, out.Stdout, out.Stderr)
		}
	}
}
