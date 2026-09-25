package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/logfile"
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

func newDaemonRunCmd(env *Env) *cobra.Command {
	var logFile string
	cmd := &cobra.Command{
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
			var out io.Writer = env.Stderr
			if logFile != "" {
				// Rotated at 10 MiB, 3 old files kept.
				f, err := logfile.Open(logFile, logfile.DefaultMaxBytes, logfile.DefaultKeep)
				if err != nil {
					return fmt.Errorf("open log file: %w", err)
				}
				defer f.Close()
				out = f
			}
			// The daemon writes daemon.pid itself once it owns the socket.
			logger := slog.New(slog.NewJSONHandler(out, nil))
			logger.Info("daemon starting", "home", paths.Home)
			if err := env.RunDaemon(ctx, paths, logger); err != nil {
				logger.Error("daemon exited", "err", err)
				return err
			}
			logger.Info("daemon stopped")
			return nil
		},
	}
	cmd.Flags().StringVar(&logFile, "log-file", "", "write the log to this file, rotated at 10 MiB (3 old files kept), instead of stderr")
	return cmd
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
				// The daemon rotates its own log; the spawn's output file only
				// catches crash output.
				if _, err := env.Spawn(exe, []string{"daemon", "run", "--log-file", paths.Log}, paths.StderrLog()); err != nil {
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
			return fmt.Errorf("daemon did not start within %s; see %s and %s", startWait, paths.Log, paths.StderrLog())
		},
	}
}

// stopWait is how long `daemon stop` waits for the daemon to go away.
var stopWait = 10 * time.Second

func newDaemonStopCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if env.Service != nil && env.Service.Installed() {
				if err := env.Service.Stop(ctx); err != nil {
					return err
				}
				fmt.Fprintln(env.Stdout, "Daemon stopped.")
				return nil
			}
			paths, err := env.Paths()
			if err != nil {
				return err
			}
			// Only a daemon that answers on the socket is stopped: a pid file
			// alone may name an unrelated process that reused the pid.
			c, err := connect(ctx, env)
			if err != nil {
				fmt.Fprintln(env.Stdout, "Daemon is not running.")
				return nil
			}
			err = c.Call(ctx, ipc.MethodStatus, nil, nil)
			if err == nil {
				err = c.Call(ctx, ipc.MethodDaemonShutdown, nil, nil)
			} else if errors.Is(err, ipc.ErrClosed) {
				c.Close()
				fmt.Fprintln(env.Stdout, "Daemon is not running.")
				return nil
			}
			c.Close()
			switch {
			case err == nil || errors.Is(err, ipc.ErrClosed):
				if err := waitGone(ctx, func() bool {
					_, err := os.Stat(paths.Socket)
					return errors.Is(err, os.ErrNotExist)
				}); err != nil {
					return err
				}
			case ipc.IsKind(err, ipc.KindBadRequest):
				// An older daemon without daemon.shutdown: signal the pid it recorded.
				if err := stopByPID(ctx, paths.PIDFile()); err != nil {
					return err
				}
			default:
				return err
			}
			fmt.Fprintln(env.Stdout, "Daemon stopped.")
			return nil
		},
	}
}

// waitGone polls gone until it reports true or stopWait passes.
func waitGone(ctx context.Context, gone func() bool) error {
	deadline := time.Now().Add(stopWait)
	for !gone() {
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon did not stop within %s", stopWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// stopByPID sends SIGTERM to the pid in pf and waits for the process to exit.
func stopByPID(ctx context.Context, pf string) error {
	b, err := os.ReadFile(pf)
	if err != nil {
		return fmt.Errorf("daemon cannot be stopped over its socket and has no readable pid file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("bad pid file %s", pf)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return waitGone(ctx, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
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
