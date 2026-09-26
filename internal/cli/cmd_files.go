package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() { Register(newFilesCmd) }

func newFilesCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "List incoming and outgoing files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				var r ipc.FilesListResult
				if err := c.Call(cmd.Context(), ipc.MethodFilesList, nil, &r); err != nil {
					return err
				}
				if len(r.Files) == 0 {
					fmt.Fprintln(env.Stdout, "No files.")
					return nil
				}
				tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "FILE ID\tDIR\tPEER\tSTATE\tSIZE\tNAME")
				for _, f := range r.Files {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", terminalSafe(f.FileID), terminalSafe(string(f.Direction)), terminalSafe(f.Peer), terminalSafe(string(f.State)), f.Size, terminalSafe(f.Name))
				}
				return tw.Flush()
			})
		},
	}
	return cmd
}
