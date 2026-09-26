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
	var noQR bool
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Create a one-time join code for another machine (asks for your password)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := connect(ctx, env)
			if err != nil {
				return err
			}
			defer c.Close()
			return pairNow(ctx, env, c, !noQR)
		},
	}
	cmd.Flags().BoolVar(&noQR, "no-qr", false, "do not draw the QR code")
	return cmd
}

func newJoinCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "join <code>",
		Short: "Join another machine using its join code or bind code (asks for your password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := connect(ctx, env)
			if err != nil {
				return err
			}
			defer c.Close()
			code, err := joinBindCode(ctx, c, args[0])
			if err != nil {
				return err
			}
			var pending ipc.PendingPeerResult
			if err := withUnlock(ctx, env, c, func() error {
				return c.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: code}, &pending)
			}); err != nil {
				return err
			}
			return finalizePeer(ctx, env, c, pending)
		},
	}
}

// pairNow creates a bind code, shows it as a join code (and QR code when qr
// is set), waits for the other machine and asks for its local name.
func pairNow(ctx context.Context, env *Env, c Caller, qr bool) error {
	var start ipc.PairStartResult
	if err := withUnlock(ctx, env, c, func() error {
		return c.Call(ctx, ipc.MethodPairStart, nil, &start)
	}); err != nil {
		return err
	}
	w := env.Stdout
	if err := printPairingCode(w, daemonRelay(ctx, c), start.Code, qr); err != nil {
		return err
	}
	fmt.Fprintln(w, "Waiting for the other machine...")
	var pending ipc.PendingPeerResult
	if err := withUnlock(ctx, env, c, func() error {
		return c.Call(ctx, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: start.PendingID}, &pending)
	}); err != nil {
		return err
	}
	return finalizePeer(ctx, env, c, pending)
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
