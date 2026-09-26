package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"text/tabwriter"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() { Register(newSessionCmd) }

// runInteractive runs argv in dir with the terminal attached (tests replace it).
var runInteractive = func(ctx context.Context, env *Env, dir string, argv []string) error {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = dir
	c.Stdin, c.Stdout, c.Stderr = env.Stdin, env.Stdout, env.Stderr
	return c.Run()
}

func newSessionCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Managed sessions on this machine: list, open and close them",
	}
	cmd.AddCommand(newSessionListCmd(env), newSessionOpenCmd(env), newSessionCloseCmd(env))
	return cmd
}

func newSessionListCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the managed sessions other machines started here",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				var r ipc.ManagedListResult
				if err := c.Call(ctx, ipc.MethodManagedList, nil, &r); err != nil {
					return err
				}
				if len(r.Sessions) == 0 {
					fmt.Fprintln(env.Stdout, "No managed sessions. A paired machine starts one by connecting to new:<label> of an offer (see cravv-connect offers).")
					return nil
				}
				tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "SESSION\tFOR\tOFFER\tSTATE\tLINK\tFOLDER")
				for _, s := range r.Sessions {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", terminalSafe(s.Name), terminalSafe(s.Machine), terminalSafe(orDash(s.Offer)),
						terminalSafe(s.State), s.Link, terminalSafe(s.Folder))
				}
				return tw.Flush()
			})
		},
	}
}

func newSessionOpenCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "open <name>",
		Short: "Open a managed session's conversation here (its queue waits until you exit)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// The connection holds the session's queue: it stays open while
			// the conversation is open, and closing it resumes the queue.
			return withConn(ctx, env, func(c Caller) error {
				fmt.Fprintf(env.Stderr, "Waiting for %s to finish any run in progress...\n", terminalSafe(args[0]))
				var r ipc.ManagedOpenResult
				if err := c.Call(ctx, ipc.MethodManagedOpen, ipc.ManagedNameParams{Name: args[0]}, &r); err != nil {
					return err
				}
				if len(r.Command) == 0 {
					return errors.New("the daemon returned no command to open")
				}
				fmt.Fprintf(env.Stderr, "Opening %s in %s. Its queue waits until you exit.\n", terminalSafe(r.Name), terminalSafe(r.Folder))
				if err := runInteractive(ctx, env, r.Folder, r.Command); err != nil {
					return fmt.Errorf("%s: %w", r.Command[0], err)
				}
				fmt.Fprintf(env.Stderr, "%s runs its queue again.\n", terminalSafe(r.Name))
				return nil
			})
		},
	}
}

func newSessionCloseCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "close <name>",
		Short: "Close a managed session (its link closes too)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				if err := c.Call(ctx, ipc.MethodManagedClose, ipc.ManagedNameParams{Name: args[0]}, nil); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, "Closed %s; its link closed too.\n", terminalSafe(args[0]))
				return nil
			})
		},
	}
}
