package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() {
	Register(newApprovalsCmd)
	Register(func(env *Env) *cobra.Command { return newDecideCmd(env, "approve", true) })
	Register(func(env *Env) *cobra.Command { return newDecideCmd(env, "deny", false) })
}

func newApprovalsCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "approvals",
		Short: "Review tasks waiting for your approval (asks for your password once)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				var r ipc.ApprovalsListResult
				if err := withUnlock(ctx, env, c, func() error {
					return c.Call(ctx, ipc.MethodApprovalsList, nil, &r)
				}); err != nil {
					return err
				}
				if len(r.Tasks) == 0 {
					fmt.Fprintln(env.Stdout, "No tasks awaiting approval.")
					return nil
				}
				for i, t := range r.Tasks {
					if err := reviewOne(ctx, env, c, i+1, len(r.Tasks), t); err != nil {
						return err
					}
				}
				return nil
			})
		},
	}
}

// reviewOne shows one pending task and applies the human's choice.
func reviewOne(ctx context.Context, env *Env, c Caller, n, total int, t ipc.ApprovalView) error {
	w := env.Stdout
	fmt.Fprintf(w, "\nTask %d of %d: %s from %s, received %s\n", n, total, t.TaskID, t.Peer, fmtTime(t.Received))
	fmt.Fprintf(w, "Size: %d bytes, SHA-256: %s\n", t.Size, t.SHA256)
	fmt.Fprintf(w, "--- preview (first %d characters) ---\n%s\n---\n", api.PreviewRunes, terminalSafe(t.Preview))
	for {
		ans, err := env.Prompt.Line("[a]pprove, [d]eny, [v]iew full text, [s]kip", "s")
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(ans)) {
		case "a", "approve":
			return decide(ctx, env, c, t.TaskID, true)
		case "d", "deny":
			return decide(ctx, env, c, t.TaskID, false)
		case "v", "view":
			fmt.Fprintf(w, "--- full text ---\n%s\n---\n", terminalSafe(t.Full))
		case "s", "skip":
			fmt.Fprintln(w, "Skipped.")
			return nil
		default:
			fmt.Fprintln(env.Stderr, "Please answer a, d, v or s.")
		}
	}
}

func decide(ctx context.Context, env *Env, c Caller, id string, approve bool) error {
	if err := withUnlock(ctx, env, c, func() error {
		return c.Call(ctx, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: id, Approve: approve}, nil)
	}); err != nil {
		return err
	}
	if approve {
		fmt.Fprintf(env.Stdout, "Approved %s.\n", id)
	} else {
		fmt.Fprintf(env.Stdout, "Denied %s.\n", id)
	}
	return nil
}

func newDecideCmd(env *Env, verb string, approve bool) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <task-id>",
		Short: strings.ToUpper(verb[:1]) + verb[1:] + " one pending task (asks for your password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				return decide(cmd.Context(), env, c, args[0], approve)
			})
		},
	}
}
