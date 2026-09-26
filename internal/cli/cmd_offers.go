package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() { Register(newOffersCmd) }

// shellWarning is shown before the typed confirmation of run mode shell.
const shellWarning = "Run mode shell: the peer can run commands as your user on this machine. " +
	"A shell run can do anything your user can, including talking to the local cravv-connect daemon without a token, " +
	"reading ~/.cravv-connect, and editing your ~/.claude settings."

func newOffersCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "offers",
		Short: "Managed-session offers: folders a paired machine may start an agent session in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listOffers(cmd.Context(), env, "")
		},
	}
	cmd.AddCommand(newOffersListCmd(env), newOffersSetCmd(env), newOffersRemoveCmd(env))
	return cmd
}

func newOffersListCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "list [machine]",
		Short: "List the offers (to one machine, or to all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			machine := ""
			if len(args) == 1 {
				machine = args[0]
			}
			return listOffers(cmd.Context(), env, machine)
		},
	}
}

func listOffers(ctx context.Context, env *Env, machine string) error {
	return withConn(ctx, env, func(c Caller) error {
		var r ipc.OffersListResult
		if err := c.Call(ctx, ipc.MethodOffersList, ipc.OffersListParams{Machine: machine}, &r); err != nil {
			return err
		}
		if len(r.Offers) == 0 {
			fmt.Fprintln(env.Stdout, "No offers. Make one with: cravv-connect offers set <machine> <label> --folder <dir> --permission tasks-auto")
			return nil
		}
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "MACHINE\tLABEL\tFOLDER\tMODE\tPERMISSION\tLIMITS")
		for _, o := range r.Offers {
			limits := fmt.Sprintf("%d open, %d/h, %d/day, run %s, idle %s", o.MaxConcurrent, o.RunsPerHour, o.RunsPerDay,
				time.Duration(o.RunTimeoutS)*time.Second, time.Duration(o.IdleTimeoutS)*time.Second)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", terminalSafe(o.Machine), terminalSafe(o.Label), terminalSafe(o.Folder),
				terminalSafe(o.RunMode), terminalSafe(o.Permission), limits)
		}
		return tw.Flush()
	})
}

func newOffersSetCmd(env *Env) *cobra.Command {
	var (
		idle, runTimeout              time.Duration
		folder, perm, mode            string
		maxOpen, turns, perHr, perDay int
		force                         bool
	)
	cmd := &cobra.Command{
		Use:   "set <machine> <label>",
		Short: "Create or change an offer (asks for your password; shell asks you to type shell)",
		Long: "Let <machine> start managed agent sessions in a folder: it connects to <this machine>/new:<label>.\n" +
			"Modes: read-only (read and search), edit-in-folder (also edit files; no shell, no web), shell (also run commands as your user).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			abs, err := absFolder(env, folder)
			if err != nil {
				return err
			}
			p := ipc.OfferSetParams{
				Machine: args[0], Label: args[1], Folder: abs, Permission: perm, RunMode: mode,
				MaxConcurrent: maxOpen, IdleTimeoutS: int(idle / time.Second), MaxTurnsPerRun: turns,
				RunTimeoutS: int(runTimeout / time.Second), RunsPerHour: perHr, RunsPerDay: perDay, Force: force,
			}
			if mode == "shell" {
				fmt.Fprintln(env.Stdout, shellWarning)
				typed, err := env.Prompt.Line("Type shell to confirm", "")
				if err != nil {
					return err
				}
				if typed != "shell" {
					return fmt.Errorf("not confirmed: nothing changed")
				}
				p.ShellConfirm = typed
			}
			return withConn(ctx, env, func(c Caller) error {
				var v ipc.OfferView
				if err := withUnlock(ctx, env, c, func() error { return c.Call(ctx, ipc.MethodOffersSet, p, &v) }); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, "Offer %s to %s: %s (%s, %s).\n", terminalSafe(v.Label), terminalSafe(v.Machine),
					terminalSafe(v.Folder), terminalSafe(v.RunMode), terminalSafe(v.Permission))
				fmt.Fprintf(env.Stdout, "On %s, a chat connects to new:%s on this machine to start a managed session.\n",
					terminalSafe(v.Machine), terminalSafe(v.Label))
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&folder, "folder", "", "the folder managed sessions work in (required)")
	f.StringVar(&perm, "permission", "", "what the other machine may do: messages or tasks-auto (required)")
	f.StringVar(&mode, "mode", "read-only", "read-only, edit-in-folder or shell")
	f.IntVar(&maxOpen, "max-concurrent", 0, "managed sessions open at once (default 2)")
	f.DurationVar(&idle, "idle-timeout", 0, "close a managed session idle this long (default 2h)")
	f.DurationVar(&runTimeout, "run-timeout", 0, "stop a run after this long (default 30m)")
	f.IntVar(&turns, "max-turns", 0, "turns per run, recorded for agents that support a limit (default 40)")
	f.IntVar(&perHr, "runs-per-hour", 0, "runs per link per hour (default 30)")
	f.IntVar(&perDay, "runs-per-day", 0, "runs per day for this machine (default 200)")
	f.BoolVar(&force, "force", false, "set it even if the daemon cannot find claude now (runs fail until it can)")
	_ = cmd.MarkFlagRequired("folder")
	_ = cmd.MarkFlagRequired("permission")
	return cmd
}

// absFolder makes folder absolute against the working directory.
func absFolder(env *Env, folder string) (string, error) {
	if filepath.IsAbs(folder) {
		return filepath.Clean(folder), nil
	}
	wd, err := env.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, folder), nil
}

func newOffersRemoveCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <machine> <label>",
		Short: "Remove an offer and close its managed sessions (asks for your password)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				p := ipc.OfferRemoveParams{Machine: args[0], Label: args[1]}
				if err := withUnlock(ctx, env, c, func() error { return c.Call(ctx, ipc.MethodOffersRemove, p, nil) }); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, "Removed offer %s to %s; its managed sessions closed.\n", terminalSafe(args[1]), terminalSafe(args[0]))
				return nil
			})
		},
	}
}

// printOffers adds a machine's offers to a sessions table; gap separates
// them from the sessions above.
func printOffers(w io.Writer, r ipc.SessionsListResult, gap bool) {
	if len(r.Offers) == 0 {
		return
	}
	if gap {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "OFFER\tMAX PERMISSION\tAGENT\tCONNECT TO")
	for _, o := range r.Offers {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s/new:%s\n", terminalSafe(o.Label), terminalSafe(o.MaxPermission), terminalSafe(orDash(o.Agent)),
			terminalSafe(r.Machine), terminalSafe(o.Label))
	}
}
