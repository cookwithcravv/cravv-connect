package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/install"
	"github.com/spf13/cobra"
)

func init() {
	Register(newInstallCmd)
	Register(newUninstallCmd)
	RegisterDaemon(newDaemonInstallCmd)
	RegisterDaemon(newDaemonUninstallCmd)
}

const otherAgentsHint = "For Cursor, VS Code and Gemini CLI, see docs/agents.md."

func agentInstaller(env *Env, name string) (install.Installer, error) {
	if env.Agents == nil {
		return nil, errors.New("agent installers are not available")
	}
	i, ok := env.Agents.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q (supported: %s). %s", name, strings.Join(env.Agents.Names(), ", "), otherAgentsHint)
	}
	return i, nil
}

func newInstallCmd(env *Env) *cobra.Command {
	var allowSend, noAllowSend bool
	cmd := &cobra.Command{
		Use:   "install [agent]",
		Short: "Add cravv-connect to a coding agent (claude, codex); no argument lists agents",
		Long: "Adds the MCP server to the agent. For Claude Code it also adds the Stop and UserPromptSubmit hooks, " +
			"the /cravv skill and allow rules for the tools that only read or act within an existing link and for " +
			"the listener; connect, create_task and send_file still ask unless you pass --allow-send. " +
			"Running it again without either flag keeps the send rules as they are; --no-allow-send removes the " +
			"ones --allow-send added. Rules you wrote yourself are never changed. " +
			"`cravv-connect uninstall <agent>` removes what it added.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return listAgents(env)
			}
			i, err := agentInstaller(env, args[0])
			if err != nil {
				return err
			}
			bin, err := env.Executable()
			if err != nil {
				return err
			}
			if allowSend && noAllowSend {
				return errors.New("--allow-send and --no-allow-send cannot be used together")
			}
			oi, withOptions := i.(install.OptionInstaller)
			switch {
			case withOptions:
				err = oi.InstallWith(cmd.Context(), bin, install.Options{AllowSend: allowSend, NoAllowSend: noAllowSend})
			case allowSend || noAllowSend:
				return fmt.Errorf("--allow-send and --no-allow-send apply to Claude Code only, not %s", i.Name())
			default:
				err = i.Install(cmd.Context(), bin)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(env.Stdout, "Installed cravv-connect for %s. Restart the agent so it loads the MCP server.\n", i.Name())
			if withOptions && !allowSend && !noAllowSend {
				fmt.Fprintln(env.Stdout, "Allow rules for connect, create_task and send_file are left as they were: "+
					"`cravv-connect install claude --allow-send` allows them without a prompt, --no-allow-send makes them ask again.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowSend, "allow-send", false, "also allow connect, create_task and send_file without a prompt (Claude Code)")
	cmd.Flags().BoolVar(&noAllowSend, "no-allow-send", false, "remove the connect, create_task and send_file rules --allow-send added (Claude Code)")
	return cmd
}

func listAgents(env *Env) error {
	if env.Agents == nil {
		return errors.New("agent installers are not available")
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSTATUS")
	for _, n := range env.Agents.Names() {
		i, _ := env.Agents.Get(n)
		status := "not found"
		if i.Detect() {
			status = "detected"
		}
		fmt.Fprintf(tw, "%s\t%s\n", n, status)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, "Run `cravv-connect install <agent>`. "+otherAgentsHint)
	return nil
}

func newUninstallCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall <agent>",
		Short: "Remove cravv-connect from a coding agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			i, err := agentInstaller(env, args[0])
			if err != nil {
				return err
			}
			if err := i.Uninstall(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintf(env.Stdout, "Removed cravv-connect from %s.\n", i.Name())
			return nil
		},
	}
}

func newDaemonInstallCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Run the daemon at login (launchd on macOS, systemd on Linux) and start it now",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if env.ServiceSetup == nil {
				return install.ErrNoServiceManager
			}
			bin, err := serviceBinary(env)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if err := env.ServiceSetup.Install(ctx, bin); err != nil {
				return err
			}
			deadline := time.Now().Add(installWait)
			for !daemonUp(ctx, env) {
				if time.Now().After(deadline) {
					return fmt.Errorf("the login service is installed but the daemon did not answer within %s; see %s",
						installWait, daemonLogHint(env))
				}
				time.Sleep(100 * time.Millisecond)
			}
			fmt.Fprintln(env.Stdout, "Daemon installed as a login service and started.")
			if runtime.GOOS == "linux" {
				fmt.Fprintln(env.Stdout, "On a headless Linux box, run `loginctl enable-linger $USER` so it keeps running after you log out.")
			}
			return nil
		},
	}
}

func newDaemonUninstallCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the daemon and remove the login service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if env.ServiceSetup == nil {
				return install.ErrNoServiceManager
			}
			if err := env.ServiceSetup.Uninstall(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(env.Stdout, "Daemon service removed.")
			return nil
		},
	}
}

// installWait is how long `daemon install` waits for the service's daemon to answer.
var installWait = 10 * time.Second

// serviceBinary is the executable the login service will run: symlinks
// resolved, and never a temporary build (`go run`, a test binary), which
// would be deleted and leave a service that cannot start.
func serviceBinary(env *Env) (string, error) {
	bin, err := env.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	if isTemporaryBinary(bin) {
		return "", fmt.Errorf("refusing to install %s: it is a temporary build that will be deleted; "+
			"install a built binary first, e.g. make build then bin/cravv-connect daemon install", bin)
	}
	return bin, nil
}

func isTemporaryBinary(bin string) bool {
	if strings.Contains(bin, "/go-build") {
		return true
	}
	tmps := []string{os.TempDir()}
	if r, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		tmps = append(tmps, r)
	}
	for _, tmp := range tmps {
		tmp = filepath.Clean(tmp)
		if tmp != "/" && strings.HasPrefix(bin, tmp+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// daemonLogHint names where a background daemon's logs are.
func daemonLogHint(env *Env) string {
	paths, err := env.Paths()
	if err != nil {
		return "the daemon log"
	}
	if runtime.GOOS == "linux" {
		return paths.Log + " and `journalctl --user -u cravv-connect`"
	}
	return paths.Log + " and " + paths.StderrLog()
}
