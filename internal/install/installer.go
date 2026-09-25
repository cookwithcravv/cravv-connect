// Package install sets cravv-connect up inside coding agents (Claude Code,
// Codex) and as a login service (launchd on macOS, systemd on Linux).
package install

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ServerName is the MCP server name used in every agent config.
const ServerName = "cravv-connect"

// Runner runs an external command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Installer adds or removes cravv-connect for one agent.
type Installer interface {
	Name() string
	// Install is idempotent: running it twice leaves the same result.
	Install(ctx context.Context, bin string) error
	Uninstall(ctx context.Context) error
	// Detect reports whether the agent seems to be present on this machine.
	Detect() bool
}

// Registry holds installers by name. New agents register; nothing else changes.
type Registry struct {
	byName map[string]Installer
}

// NewRegistry returns a registry holding the given installers.
func NewRegistry(installers ...Installer) *Registry {
	r := &Registry{byName: map[string]Installer{}}
	for _, i := range installers {
		r.Register(i)
	}
	return r
}

// Register adds an installer, replacing one with the same name.
func (r *Registry) Register(i Installer) { r.byName[i.Name()] = i }

// Get returns the installer for name.
func (r *Registry) Get(name string) (Installer, bool) {
	i, ok := r.byName[name]
	return i, ok
}

// Names returns the registered names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// DefaultRegistry returns the Claude Code and Codex installers for the user's
// home directory.
func DefaultRegistry(home string, run Runner) *Registry {
	return NewRegistry(
		&Claude{Home: home, Run: run, LookPath: exec.LookPath},
		&Codex{Home: home, LookPath: exec.LookPath},
	)
}

// writeFileAtomic writes data to path through a temp file and rename, keeping
// the existing file's permissions (perm for a new file).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// shellQuote quotes a path for a shell command line when needed.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
