package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// Factory builds one top-level command. Command files register factories in
// init(), so adding a command never edits this file.
type Factory func(env *Env) *cobra.Command

var (
	factories       []Factory
	daemonFactories []Factory
)

// Register adds a top-level command.
func Register(f Factory) { factories = append(factories, f) }

// RegisterDaemon adds a `daemon <sub>` command.
func RegisterDaemon(f Factory) { daemonFactories = append(daemonFactories, f) }

// errSilent makes Main exit 1 without printing (the command already printed).
var errSilent = errors.New("silent failure")

// NewRoot builds the command tree for env.
func NewRoot(env *Env) *cobra.Command {
	root := &cobra.Command{
		Use:           "cravv-connect",
		Short:         "Connect coding agents on different machines, end-to-end encrypted",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(env.Stdin)
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	for _, f := range factories {
		root.AddCommand(f(env))
	}
	return root
}

// Main runs the CLI and returns the process exit code.
func Main(args []string, env *Env) int {
	root := NewRoot(env)
	root.SetArgs(args)
	if err := root.ExecuteContext(context.Background()); err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintln(env.Stderr, "error:", userMessage(err))
		}
		return 1
	}
	return 0
}
