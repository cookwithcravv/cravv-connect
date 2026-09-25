package cli

import (
	"fmt"
	"strings"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() {
	Register(newStatusCmd)
	Register(newLogCmd)
}

func newStatusCmd(env *Env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show relay connection, peers, queues and pending approvals",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				var st ipc.StatusResult
				if err := c.Call(cmd.Context(), ipc.MethodStatus, nil, &st); err != nil {
					return err
				}
				if asJSON {
					return printJSON(env.Stdout, st)
				}
				printStatus(env, st)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printStatus(env *Env, st ipc.StatusResult) {
	w := env.Stdout
	relay := "offline"
	if st.RelayConnected {
		relay = "connected"
	}
	kill := "off"
	if st.Killed {
		kill = "ON (run `cravv-connect resume`)"
	}
	fmt.Fprintf(w, "Machine:     %s (%s)\n", terminalSafe(st.DeviceName), terminalSafe(shortID(st.MachineID)))
	fmt.Fprintf(w, "Relay:       %s (%s)\n", terminalSafe(st.RelayURL), relay)
	fmt.Fprintf(w, "Kill switch: %s\n", kill)
	peers := make([]string, 0, len(st.Peers))
	for _, p := range st.Peers {
		peers = append(peers, fmt.Sprintf("%s (%s, %s)", terminalSafe(p.Alias), terminalSafe(string(p.TrustIn)), peerState(p)))
	}
	if len(peers) == 0 {
		peers = append(peers, "none")
	}
	fmt.Fprintf(w, "Peers:       %s\n", strings.Join(peers, ", "))
	sessions := "none"
	if len(st.Sessions) > 0 {
		safe := make([]string, len(st.Sessions))
		for i, s := range st.Sessions {
			safe[i] = terminalSafe(s)
		}
		sessions = strings.Join(safe, ", ")
	}
	fmt.Fprintf(w, "Sessions:    %s\n", sessions)
	fmt.Fprintf(w, "Outbox:      %d pending, %d held\n", st.OutboxPending, st.OutboxHeld)
	fmt.Fprintf(w, "Inbox:       %d unread\n", st.InboxUnread)
	fmt.Fprintf(w, "Approvals:   %d pending\n", st.PendingApprovals)
	for _, e := range st.Errors {
		fmt.Fprintf(w, "Warning:     %s\n", terminalSafe(e))
	}
}

func newLogCmd(env *Env) *cobra.Command {
	var n int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show recent audit log entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				var r ipc.AuditReadResult
				if err := c.Call(cmd.Context(), ipc.MethodAuditRead, ipc.AuditReadParams{Limit: n}, &r); err != nil {
					return err
				}
				for _, e := range r.Events {
					who := e.Alias
					if who == "" && e.Peer != "" {
						who = e.Peer.Short()
					}
					fmt.Fprintf(env.Stdout, "%s  %-16s %-12s %s\n", e.TS.UTC().Format("2006-01-02T15:04:05Z"), terminalSafe(string(e.Type)), orDash(terminalSafe(who)), orDash(terminalSafe(e.ItemID)))
				}
				return nil
			})
		},
	}
	cmd.Flags().IntVarP(&n, "lines", "n", 20, "number of entries")
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
