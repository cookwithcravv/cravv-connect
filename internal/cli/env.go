// Package cli implements the cravv-connect command line: human commands, the
// JSON agent commands, and the entry points for the daemon, mcp and hook modes.
package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"github.com/cravv/cravv-connect/internal/app"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/install"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/store/sqlite"
)

// Caller is the daemon connection the commands use (satisfied by *ipc.Client).
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
	Close() error
}

// ServiceManager starts and stops an installed launchd or systemd service.
// Task 24 provides the implementation; nil means none is available.
type ServiceManager interface {
	Installed() bool
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Env holds every side effect the commands need, so tests can replace them.
type Env struct {
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Prompt       Prompter
	Paths        func() (config.Paths, error)
	Dial         func(ctx context.Context) (Caller, error)
	Getwd        func() (string, error)
	Hostname     func() (string, error)
	OpenSettings func(dbPath string) (store.SettingsStore, func() error, error)
	RunDaemon    func(ctx context.Context, paths config.Paths, logger *slog.Logger) error
	Spawn        func(exe string, args []string, logPath string) (pid int, err error)
	Executable   func() (string, error)
	Service      ServiceManager
	ServiceSetup ServiceInstaller  // installs the login service; nil when unsupported
	Agents       *install.Registry // agent installers; nil disables `install`
	Setup        SetupSystem       // network and processes for `setup`; nil uses the real ones
}

// ServiceInstaller installs and removes the login service.
type ServiceInstaller interface {
	Install(ctx context.Context, bin string) error
	Uninstall(ctx context.Context) error
}

// DefaultEnv wires the real terminal, socket, store and process functions.
func DefaultEnv() *Env {
	env := &Env{
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Prompt:     TTYPrompter{},
		Paths:      config.ResolvePaths,
		Getwd:      os.Getwd,
		Hostname:   os.Hostname,
		RunDaemon:  app.Run,
		Spawn:      spawnDetached,
		Executable: os.Executable,
		OpenSettings: func(dbPath string) (store.SettingsStore, func() error, error) {
			db, err := sqlite.Open(dbPath)
			if err != nil {
				return nil, nil, err
			}
			return db, db.Close, nil
		},
	}
	env.Dial = func(ctx context.Context) (Caller, error) {
		p, err := env.Paths()
		if err != nil {
			return nil, err
		}
		return ipc.DialContext(ctx, p.Socket)
	}
	return env
}

// spawnDetached starts exe in its own session with output appended to logPath.
func spawnDetached(exe string, args []string, logPath string) (int, error) {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	return pid, cmd.Process.Release()
}
