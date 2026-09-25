package cli

import (
	"context"
	"path/filepath"

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
			return mcpserver.Run(cmd.Context(), mcpserver.Options{
				Dial: func(ctx context.Context) (mcpserver.Conn, error) {
					p, err := env.Paths()
					if err != nil {
						return nil, err
					}
					return ipc.DialContext(ctx, p.Socket)
				},
				ProjectDir: dir,
				Version:    Version,
			})
		},
	}
	cmd.Flags().StringVar(&projectDir, "project-dir", "", "project folder for this session (default: the current directory)")
	return cmd
}

// mcpProjectDir returns the absolute project folder: the flag, else the
// working directory.
func mcpProjectDir(env *Env, flag string) (string, error) {
	if flag == "" {
		return env.Getwd()
	}
	return filepath.Abs(flag)
}
