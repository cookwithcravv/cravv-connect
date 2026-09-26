package cli

import (
	"context"
	"fmt"
	"strconv"
	"text/tabwriter"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() {
	Register(newLinksCmd)
	Register(newLinkCmd)
	Register(newSessionsCmd)
}

// linkState is the STATE column: pending requests say who decides, closed
// links say why.
func linkState(l ipc.LinkView) string {
	switch {
	case l.State == "pending" && l.Direction == "in":
		return "request (you decide)"
	case l.State == "pending":
		return "requested (they decide)"
	case l.State == "closed":
		return "closed: " + l.Reason
	case l.RemoteAway:
		return "active (peer away)"
	}
	return l.State
}

// theyMay is what the peer may do on this side; for a request, what it asks.
func theyMay(l ipc.LinkView) string {
	if l.State == "pending" && l.Direction == "in" {
		return "asks " + l.Proposed
	}
	if l.PermissionIn == "" {
		return "-"
	}
	return l.PermissionIn
}

func newLinksCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "links",
		Short: "List the links between this machine's sessions and sessions on paired machines",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				var r ipc.LinksResult
				if err := c.Call(cmd.Context(), ipc.MethodLinks, nil, &r); err != nil {
					return err
				}
				if len(r.Links) == 0 {
					fmt.Fprintln(env.Stdout, "No links yet. A shared chat asks for one with its connect tool.")
					return nil
				}
				tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "LINK\tSESSION\tPEER\tSTATE\tTHEY MAY\tYOU MAY")
				for _, l := range r.Links {
					fmt.Fprintf(tw, "%d\t%s\t%s/%s\t%s\t%s\t%s\n", l.Link, terminalSafe(orDash(l.Session)),
						terminalSafe(l.Machine), terminalSafe(l.RemoteSession), terminalSafe(linkState(l)),
						terminalSafe(theyMay(l)), terminalSafe(orDash(l.PermissionOut)))
				}
				return tw.Flush()
			})
		},
	}
}

func newLinkCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Decide link requests and raise what a link allows",
	}
	cmd.AddCommand(newLinkAcceptCmd(env), newLinkRejectCmd(env), newLinkPermitCmd(env))
	return cmd
}

func linkArg(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a link number (see cravv-connect links)", s)
	}
	return n, nil
}

// findLink returns link num from the full list.
func findLink(ctx context.Context, c Caller, num int64) (ipc.LinkView, error) {
	var r ipc.LinksResult
	if err := c.Call(ctx, ipc.MethodLinks, nil, &r); err != nil {
		return ipc.LinkView{}, err
	}
	for _, l := range r.Links {
		if l.Link == num {
			return l, nil
		}
	}
	return ipc.LinkView{}, fmt.Errorf("no link %d (see cravv-connect links)", num)
}

func newLinkAcceptCmd(env *Env) *cobra.Command {
	var perm string
	cmd := &cobra.Command{
		Use:   "accept <link>",
		Short: "Accept a link request (asks for your password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			num, err := linkArg(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				l, err := findLink(ctx, c, num)
				if err != nil {
					return err
				}
				w := env.Stdout
				fmt.Fprintf(w, "Link %d: %s/%s asks to link with your session %s, with permission %s.\n",
					l.Link, terminalSafe(l.Machine), terminalSafe(l.RemoteSession), terminalSafe(orDash(l.Session)), terminalSafe(l.Proposed))
				if l.Wrapped != "" {
					fmt.Fprintf(w, "--- from the other machine ---\n%s\n---\n", terminalBlock(l.Wrapped))
				}
				var v ipc.LinkView
				if err := withUnlock(ctx, env, c, func() error {
					return c.Call(ctx, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: num, Accept: true, Permission: perm}, &v)
				}); err != nil {
					return err
				}
				fmt.Fprintf(w, "Accepted link %d: %s/%s may now use %s.\n", v.Link, terminalSafe(v.Machine), terminalSafe(v.RemoteSession), terminalSafe(v.PermissionIn))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&perm, "permission", "", "grant less than asked: messages, tasks-ask or tasks-auto")
	return cmd
}

func newLinkRejectCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "reject <link>",
		Short: "Reject a link request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			num, err := linkArg(args[0])
			if err != nil {
				return err
			}
			return withConn(cmd.Context(), env, func(c Caller) error {
				if err := c.Call(cmd.Context(), ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: num}, nil); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, "Rejected link %d.\n", num)
				return nil
			})
		},
	}
}

func newLinkPermitCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "permit <link> <messages|tasks-ask|tasks-auto>",
		Short: "Set what the other side of a link may do here (raising asks for your password)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			num, err := linkArg(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				var v ipc.LinkView
				if err := withUnlock(ctx, env, c, func() error {
					return c.Call(ctx, ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: num, Permission: args[1]}, &v)
				}); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, "Link %d now allows %s.\n", v.Link, terminalSafe(v.PermissionIn))
				return nil
			})
		},
	}
}

func newSessionsCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "sessions <machine>",
		Short: "List the sessions a paired machine lets this machine see",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				var r ipc.SessionsListResult
				if err := c.Call(cmd.Context(), ipc.MethodSessionsList, ipc.MachineParams{Machine: args[0]}, &r); err != nil {
					return err
				}
				if len(r.Sessions) == 0 {
					fmt.Fprintf(env.Stdout, "%s shows you no sessions.\n", terminalSafe(r.Machine))
					return nil
				}
				tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "SESSION\tSTATE\tKIND\tAGENT")
				for _, s := range r.Sessions {
					fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\n", terminalSafe(r.Machine), terminalSafe(s.Name),
						terminalSafe(s.State), terminalSafe(s.Kind), terminalSafe(orDash(s.Agent)))
				}
				return tw.Flush()
			})
		},
	}
}
