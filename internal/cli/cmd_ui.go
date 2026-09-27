package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/webui"
	"github.com/spf13/cobra"
)

func init() { Register(newUICmd) }

// openBrowser opens url in the default browser. Tests replace it.
var openBrowser = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func newUICmd(env *Env) *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the local web UI in your browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				var r ipc.UIStartResult
				if err := c.Call(ctx, ipc.MethodUIStart, nil, &r); err != nil {
					return err
				}
				w := env.Stdout
				fmt.Fprintln(w, "cravv-connect UI:")
				fmt.Fprintf(w, "  %s\n", terminalSafe(r.URL))
				fmt.Fprintf(w, "The link works once, within %d minutes. Run cravv-connect ui again for a new one.\n", int(webui.LaunchTokenTTL.Minutes()))
				if noBrowser {
					return nil
				}
				if err := openBrowser(r.URL); err != nil {
					fmt.Fprintf(env.Stderr, "Could not open a browser (%s). Open the link above yourself.\n", terminalSafe(err.Error()))
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the link without opening a browser")
	return cmd
}
