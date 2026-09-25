package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

func init() { Register(newDaemonCmd) }

func init() {
	RegisterDaemon(newDaemonRunCmd)
	RegisterDaemon(newDaemonStartCmd)
	RegisterDaemon(newDaemonStopCmd)
	RegisterDaemon(newDaemonStatusCmd)
}

// startWait is how long `daemon start` waits for the socket to answer.
var startWait = 5 * time.Second

func newDaemonCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "Run or control the background daemon"}
	for _, f := range daemonFactories {
		cmd.AddCommand(f(env))
	}
	return cmd
}

func pidFile(env *Env) (string, error) {
	p, err := env.Paths()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Home, "daemon.pid"), nil
}

func newDaemonRunCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := env.Paths()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			pf := filepath.Join(paths.Home, "daemon.pid")
			if err := os.WriteFile(pf, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
				return err
			}
			defer os.Remove(pf)
			logger := slog.New(slog.NewJSONHandler(env.Stderr, nil))
			logger.Info("daemon starting", "home", paths.Home)
			return env.RunDaemon(ctx, paths, logger)
		},
	}
}

// daemonUp reports whether the daemon answers on its socket.
func daemonUp(ctx context.Context, env *Env) bool {
	c, err := connect(ctx, env)
	if err != nil {
		return false
	}
	defer c.Close()
	return c.Call(ctx, ipc.MethodStatus, nil, nil) == nil
}

func newDaemonStartCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon (through launchd or systemd when installed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if daemonUp(ctx, env) {
				fmt.Fprintln(env.Stdout, "Daemon is already running.")
				return nil
			}
			if env.Service != nil && env.Service.Installed() {
				if err := env.Service.Start(ctx); err != nil {
					return err
				}
			} else {
				exe, err := env.Executable()
				if err != nil {
					return err
				}
				paths, err := env.Paths()
				if err != nil {
					return err
				}
				if _, err := env.Spawn(exe, []string{"daemon", "run"}, paths.Log); err != nil {
					return err
				}
			}
			deadline := time.Now().Add(startWait)
			for time.Now().Before(deadline) {
				if daemonUp(ctx, env) {
					fmt.Fprintln(env.Stdout, "Daemon started.")
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			paths, _ := env.Paths()
			return fmt.Errorf("daemon did not start within %s; see %s", startWait, paths.Log)
		},
	}
}

func newDaemonStopCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if env.Service != nil && env.Service.Installed() {
				if err := env.Service.Stop(cmd.Context()); err != nil {
					return err
				}
				fmt.Fprintln(env.Stdout, "Daemon stopped.")
				return nil
			}
			pf, err := pidFile(env)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(pf)
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintln(env.Stdout, "Daemon is not running.")
				return nil
			}
			if err != nil {
				return err
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 0 {
				return fmt.Errorf("bad pid file %s", pf)
			}
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				if errors.Is(err, syscall.ESRCH) {
					os.Remove(pf)
					fmt.Fprintln(env.Stdout, "Daemon is not running.")
					return nil
				}
				return err
			}
			fmt.Fprintln(env.Stdout, "Daemon stopped.")
			return nil
		},
	}
}

func newDaemonStatusCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the daemon is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := connect(ctx, env)
			if err != nil {
				fmt.Fprintln(env.Stdout, "Daemon is not running.")
				return nil
			}
			defer c.Close()
			var st ipc.StatusResult
			if err := c.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
				return err
			}
			relay := "offline"
			if st.RelayConnected {
				relay = "connected"
			}
			fmt.Fprintf(env.Stdout, "Daemon is running (relay %s).\n", relay)
			return nil
		},
	}
}
