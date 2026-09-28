package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() {
	Register(newKillCmd)
	Register(newResumeCmd)
	Register(newAllowPathCmd)
	Register(newResetIdentityCmd)
}

func newKillCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "kill",
		Short: "Kill switch: stop all traffic and reject agent operations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConn(cmd.Context(), env, func(c Caller) error {
				if err := c.Call(cmd.Context(), ipc.MethodKill, nil, nil); err != nil {
					return err
				}
				fmt.Fprintln(env.Stdout, "Kill switch is on. All traffic stopped.")
				fmt.Fprintln(env.Stdout, "Run `cravv-connect resume` to turn it off (asks for your password).")
				return nil
			})
		},
	}
}

func newResumeCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "resume",
		Short: "Turn the kill switch off (asks for your password)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				if err := withUnlock(ctx, env, c, func() error { return c.Call(ctx, ipc.MethodResume, nil, nil) }); err != nil {
					return err
				}
				fmt.Fprintln(env.Stdout, "Kill switch is off. Traffic resumed.")
				return nil
			})
		},
	}
}

func newAllowPathCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "allow-path <dir>",
		Short: "Let agents send files from an extra folder (asks for your password)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			return withConn(ctx, env, func(c Caller) error {
				if err := withUnlock(ctx, env, c, func() error {
					return c.Call(ctx, ipc.MethodAllowPathAdd, ipc.AllowPathParams{Path: dir}, nil)
				}); err != nil {
					return err
				}
				fmt.Fprintf(env.Stdout, "Agents may now send files from %s.\n", dir)
				return nil
			})
		},
	}
}

func newResetIdentityCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "reset-identity",
		Short: "Create a new machine identity; every peer must pair again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				var st ipc.StatusResult
				if err := c.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
					return err
				}
				short := shortID(st.MachineID)
				fmt.Fprintln(env.Stdout, "This replaces this machine's identity key. Every peer is unpaired and must be paired again.")
				ans, err := env.Prompt.Line(fmt.Sprintf("To confirm, type this machine's short ID (%s)", short), "")
				if err != nil {
					return err
				}
				if strings.TrimSpace(ans) != short {
					return errors.New("confirmation did not match; nothing changed")
				}
				if err := unlock(ctx, env, c); err != nil {
					return err
				}
				var res ipc.ResetIdentityResult
				if err := c.Call(ctx, ipc.MethodResetIdentity, nil, &res); err != nil {
					return err
				}
				fmt.Fprintln(env.Stdout, "Identity reset. Pair with your peers again using `cravv-connect pair`.")
				if len(res.Untold) > 0 {
					fmt.Fprintf(env.Stdout, "These peers could not be told (this machine was offline or killed): %s.\n"+
						"They still list the old machine: on each of them, run `cravv-connect unpair <alias>` for this machine.\n",
						strings.Join(res.Untold, ", "))
				}
				return nil
			})
		},
	}
}
