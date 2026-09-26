package cli

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() {
	Register(newPairCmd)
	Register(newJoinCmd)
}

func newPairCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "Create a one-time bind code for another machine (asks for your password)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := connect(ctx, env)
			if err != nil {
				return err
			}
			defer c.Close()
			var start ipc.PairStartResult
			if err := withUnlock(ctx, env, c, func() error {
				return c.Call(ctx, ipc.MethodPairStart, nil, &start)
			}); err != nil {
				return err
			}
			w := env.Stdout
			fmt.Fprintf(w, "Bind code: %s\n\n", start.Code)
			fmt.Fprintln(w, "On the other machine run:")
			fmt.Fprintf(w, "  cravv-connect join %s\n", start.Code)
			fmt.Fprintln(w, "The code works once and expires in 10 minutes.")
			fmt.Fprintln(w, "Waiting for the other machine...")
			var pending ipc.PendingPeerResult
			if err := withUnlock(ctx, env, c, func() error {
				return c.Call(ctx, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: start.PendingID}, &pending)
			}); err != nil {
				return err
			}
			return finalizePeer(ctx, env, c, pending)
		},
	}
}

func newJoinCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "join <code>",
		Short: "Join another machine using its bind code (asks for your password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := connect(ctx, env)
			if err != nil {
				return err
			}
			defer c.Close()
			var pending ipc.PendingPeerResult
			if err := withUnlock(ctx, env, c, func() error {
				return c.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: args[0]}, &pending)
			}); err != nil {
				return err
			}
			return finalizePeer(ctx, env, c, pending)
		},
	}
}

// finalizePeer asks the human for a local alias. Pairing lets the two
// machines discover shared sessions and ask for links; each link is decided
// on its own.
func finalizePeer(ctx context.Context, env *Env, c Caller, p ipc.PendingPeerResult) error {
	w := env.Stdout
	fmt.Fprintf(w, "Connected to machine %s.\n", terminalSafe(shortID(string(p.MachineID))))
	alias, err := env.Prompt.Line("Local name for this peer", suggestAlias(p.SuggestedName))
	if err != nil {
		return err
	}
	var res ipc.PairFinalizeResult
	if err := withUnlock(ctx, env, c, func() error {
		return c.Call(ctx, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: p.PendingID, Alias: alias}, &res)
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "Paired with %s. Its sessions can now ask to link with yours; you decide each link.\n", terminalSafe(res.Alias))
	fmt.Fprintf(w, "Machine ID: %s\n", terminalSafe(string(p.MachineID)))
	return nil
}
