package cli

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() {
	Register(newPeersCmd)
	Register(newAliasCmd)
	Register(newPauseCmd)
	Register(newResumePeerCmd)
	Register(newUnpairCmd)
}

// withConn dials the daemon, runs fn, and closes the connection.
func withConn(ctx context.Context, env *Env, fn func(c Caller) error) error {
	c, err := connect(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()
	return fn(c)
}

func peerState(p ipc.PeerView) string {
	switch {
	case p.Paused:
		return "paused"
	case p.PausedByPeer:
		return "paused by peer"
	case p.Online:
		return "online"
	default:
		return "offline"
	}
}

func newPeersCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "peers",
		Short: "List paired machines",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				var r ipc.PeerListResult
				if err := c.Call(cmd.Context(), ipc.MethodPeerList, nil, &r); err != nil {
					return err
				}
				if len(r.Peers) == 0 {
					fmt.Fprintln(env.Stdout, "No peers yet. Run `cravv-connect pair` to add one.")
					return nil
				}
				tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "ALIAS\tSTATE\tMACHINE ID\tPAIRED")
				for _, p := range r.Peers {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", terminalSafe(p.Alias), peerState(p), terminalSafe(string(p.MachineID)), fmtTime(p.PairedAt))
				}
				return tw.Flush()
			})
		},
	}
}

func newAliasCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "alias <alias> <new-alias>",
		Short: "Rename a peer locally",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				if err := c.Call(cmd.Context(), ipc.MethodPeerAlias, ipc.PeerAliasParams{Alias: args[0], NewAlias: args[1]}, nil); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, "Renamed %s to %s.\n", args[0], args[1])
				return nil
			})
		},
	}
}

// aliasAction builds pause, resume-peer and unpair style commands.
func aliasAction(env *Env, use, short, method, done string, confirm bool) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   use + " <alias>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if confirm && !yes {
				ans, err := env.Prompt.Line(fmt.Sprintf("Unpair %s? Reconnecting needs a new bind code. Type yes to confirm", args[0]), "no")
				if err != nil {
					return err
				}
				if strings.ToLower(ans) != "yes" {
					fmt.Fprintln(env.Stdout, "Nothing changed.")
					return nil
				}
			}
			return withConn(cmd.Context(), env, func(c Caller) error {
				if err := c.Call(cmd.Context(), method, ipc.AliasParams{Alias: args[0]}, nil); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, done+"\n", args[0])
				return nil
			})
		},
	}
	if confirm {
		cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	}
	return cmd
}

func newPauseCmd(env *Env) *cobra.Command {
	return aliasAction(env, "pause", "Stop all traffic with a peer until resume-peer", ipc.MethodPeerPause, "Paused %s.", false)
}

func newResumePeerCmd(env *Env) *cobra.Command {
	return aliasAction(env, "resume-peer", "Resume traffic with a paused peer", ipc.MethodPeerResume, "Resumed %s.", false)
}

func newUnpairCmd(env *Env) *cobra.Command {
	return aliasAction(env, "unpair", "Remove a peer and delete its keys", ipc.MethodPeerUnpair, "Unpaired %s.", true)
}
