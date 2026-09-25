package cli

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
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

// finalizePeer asks the human for a local alias and a trust level.
func finalizePeer(ctx context.Context, env *Env, c Caller, p ipc.PendingPeerResult) error {
	w := env.Stdout
	fmt.Fprintf(w, "Connected to machine %s.\n", shortID(p.MachineID))
	alias, err := env.Prompt.Line("Local name for this peer", suggestAlias(p.SuggestedName))
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Trust levels: chat-only (messages only), ask-first (you approve each task), autonomous (tasks run without asking).")
	var trust string
	for try := 0; ; try++ {
		trust, err = env.Prompt.Line("Trust level", core.TrustAskFirst.String())
		if err != nil {
			return err
		}
		if _, perr := core.ParseTrust(trust); perr == nil {
			break
		}
		if try == 2 {
			return fmt.Errorf("unknown trust level %q", trust)
		}
		fmt.Fprintln(env.Stderr, "Please type chat-only, ask-first or autonomous.")
	}
	var res ipc.PairFinalizeResult
	if err := withUnlock(ctx, env, c, func() error {
		return c.Call(ctx, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: p.PendingID, Alias: alias, Trust: trust}, &res)
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "Paired with %s (trust: %s).\n", res.Alias, trust)
	fmt.Fprintf(w, "Machine ID: %s\n", p.MachineID)
	return nil
}
