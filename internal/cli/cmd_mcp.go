package cli

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/mcpserver"
	"github.com/spf13/cobra"
)

// Version is set at build time with -ldflags "-X github.com/cravv/cravv-connect/internal/cli.Version=v1.0.0".
var Version = "dev"

func init() { Register(newMCPCmd) }

func newMCPCmd(env *Env) *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the stdio MCP server (started by your coding agent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := mcpProjectDir(env, projectDir)
			if err != nil {
				return err
			}
			paths, err := env.Paths()
			if err != nil {
				return err
			}
			// An agent that quits may stop the server with SIGTERM or
			// SIGINT: stop serving then, so Run's cleanup (the wake file)
			// still happens.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err = mcpserver.Run(ctx, mcpserver.Options{
				Dial: func(ctx context.Context) (mcpserver.Conn, error) {
					p, err := env.Paths()
					if err != nil {
						return nil, err
					}
					return ipc.DialContext(ctx, p.Socket)
				},
				ProjectDir:      dir,
				Version:         Version,
				AgentSession:    agentSessionFromEnv(os.Getenv),
				WakeDir:         filepath.Join(paths.Home, "wake"),
				ListenerProgram: listenerProgram(env, exec.LookPath),
				RunToken:        os.Getenv(daemon.EnvRunToken),
			})
			if ctx.Err() != nil && cmd.Context().Err() == nil {
				return nil // stopped by a signal
			}
			return err
		},
	}
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "project folder for this session (default: the current directory)")
	return cmd
}

// listenerProgram is how the listener command names this binary: plain
// cravv-connect when that is what PATH finds (it matches the allow rule
// Bash(cravv-connect listen:*)), else the absolute path.
func listenerProgram(env *Env, lookPath func(string) (string, error)) string {
	if env.Executable == nil {
		return "cravv-connect"
	}
	self, err := env.Executable()
	if err != nil {
		return "cravv-connect"
	}
	if found, err := lookPath("cravv-connect"); err == nil && sameFile(found, self) {
		return "cravv-connect"
	}
	return self
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// agentSessionEnv lists the variables agents set to their chat ID for the
// MCP servers they start, in order of preference. Claude Code sets
// CLAUDE_CODE_SESSION_ID (seen in 2.1.28x; not documented).
var agentSessionEnv = []string{"CLAUDE_CODE_SESSION_ID", "CLAUDE_SESSION_ID"}

// agentSessionFromEnv returns the agent's chat ID, or "".
func agentSessionFromEnv(getenv func(string) string) string {
	for _, k := range agentSessionEnv {
		if v := getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// mcpProjectDir returns the absolute project folder: the flag, else the
// working directory.
func mcpProjectDir(env *Env, flag string) (string, error) {
	if flag == "" {
		return env.Getwd()
	}
	return filepath.Abs(flag)
}
