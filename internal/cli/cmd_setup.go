package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/joincode"
	"github.com/cravv/cravv-connect/internal/relayaddr"
	"github.com/spf13/cobra"
)

func init() { Register(newSetupCmd) }

// setupOptions are the flags of `cravv-connect setup`.
type setupOptions struct {
	relay, token, name string
	yes, noAgents      bool
	pair, reset        bool
	join               string
}

func newSetupCmd(env *Env) *cobra.Command {
	var o setupOptions
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set this machine up step by step: relay, daemon, agents, pairing (safe to run again)",
		Long: "Set this machine up step by step: the relay (or a LAN test relay), init, the daemon as a login service,\n" +
			"the agent integrations, and pairing a device. Run it again at any time: it shows what is set up and\n" +
			"offers the missing steps. On another machine, use `cravv-connect setup --join <code>` with the join\n" +
			"code this machine shows.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd.Context(), env, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.relay, "relay", "", "relay URL, for example https://relay.example.com")
	f.StringVar(&o.token, "relay-token", "", "relay admin token, only for the relay's first machine; use - to read it from stdin")
	f.StringVar(&o.name, "name", "", "device name suggested to peers (default: host name)")
	f.BoolVarP(&o.yes, "yes", "y", false, "answer yes to every question (needs --relay on a new machine)")
	f.BoolVar(&o.noAgents, "no-agents", false, "do not set up agent integrations")
	f.BoolVar(&o.pair, "pair", false, "pair a device at the end without asking")
	f.BoolVar(&o.reset, "reset", false, "set this machine up again from the start (asks first)")
	f.StringVar(&o.join, "join", "", "join the machine that showed this join code (cravv-join:...), on its relay")
	cmd.MarkFlagsMutuallyExclusive("join", "relay")
	cmd.MarkFlagsMutuallyExclusive("join", "relay-token")
	cmd.MarkFlagsMutuallyExclusive("join", "pair")
	return cmd
}

// setupOnlineWait is how long setup waits for the daemon to reach the relay.
var setupOnlineWait = 30 * time.Second

// setup runs the wizard. Each step checks what is already done, so running
// it again only offers what is missing.
type setup struct {
	ctx   context.Context
	env   *Env
	sys   SetupSystem
	o     setupOptions
	w     io.Writer
	paths config.Paths

	// resetting is set when --reset was agreed on a set-up machine: the
	// daemon is stopped only once the new relay answers and is confirmed
	// (stopForReset), so an aborted reset leaves it running.
	resetting bool
	// stopped is set once stopForReset stopped a running daemon.
	stopped bool
}

func runSetup(ctx context.Context, env *Env, o setupOptions) error {
	paths, err := env.Paths()
	if err != nil {
		return err
	}
	s := &setup{ctx: ctx, env: env, sys: env.setupSystem(), o: o, w: env.Stdout, paths: paths}
	var jc *joincode.Code
	if o.join != "" {
		c, err := setupJoinCode(o.join)
		if err != nil {
			return err
		}
		jc = &c
	}
	cfg, configured, err := s.current()
	if err != nil {
		return err
	}
	if o.reset && configured {
		ok, err := s.agree(fmt.Sprintf("Set this machine up again? It is set up for relay %s; on a new relay you pair again with every peer.", cfg.RelayURL), false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(s.w, "Nothing changed.")
			return nil
		}
		s.resetting, configured = true, false
	}
	if jc != nil {
		err = s.joinFlow(*jc, cfg, configured)
	} else {
		err = s.hostFlow(cfg, configured)
	}
	return s.finishReset(cfg, err)
}

// stopForReset stops a running daemon before init writes the new relay; it
// is called only after the new relay answers and was confirmed.
func (s *setup) stopForReset() error {
	if !s.resetting || !daemonUp(s.ctx, s.env) {
		return nil
	}
	s.stopped = true
	return s.run(newDaemonStopCmd)
}

// finishReset handles a reset that did not finish: before the daemon was
// stopped nothing changed, and the error says so; after, the daemon is
// started again so the machine is not left without it.
func (s *setup) finishReset(old config.Config, err error) error {
	if !s.resetting {
		return err
	}
	if !s.stopped {
		if err != nil {
			return fmt.Errorf("%w. Nothing changed: this machine still uses relay %s", err, old.RelayURL)
		}
		return nil
	}
	ctx := context.WithoutCancel(s.ctx)
	if daemonUp(ctx, s.env) {
		return err
	}
	cmd := newDaemonStartCmd(s.env)
	cmd.SetContext(ctx)
	if serr := cmd.RunE(cmd, nil); serr != nil {
		fmt.Fprintf(s.w, "Setup did not finish, and the daemon could not be started again: %v. Run `cravv-connect daemon start`.\n", serr)
	} else {
		fmt.Fprintln(s.w, "Setup did not finish, so the daemon was started again.")
	}
	return err
}

// current loads this machine's configuration; configured is false when no
// config.toml exists yet.
func (s *setup) current() (config.Config, bool, error) {
	if _, err := os.Stat(s.paths.Config); errors.Is(err, os.ErrNotExist) {
		return config.Config{}, false, nil
	}
	cfg, err := config.Load(s.paths)
	return cfg, err == nil, err
}

func (s *setup) hostFlow(cfg config.Config, configured bool) error {
	if configured {
		if s.o.relay != "" {
			want, err := relayOrigin(s.o.relay)
			if err != nil {
				return err
			}
			if have, _ := relayOrigin(cfg.RelayURL); have != want {
				return fmt.Errorf("this machine is set up for relay %s; to move it to %s, run `cravv-connect setup --reset --relay %s` (you pair again with every peer)", cfg.RelayURL, want, want)
			}
		}
		fmt.Fprintf(s.w, "This machine is set up for relay %s as %s.\n", cfg.RelayURL, cfg.DeviceName)
	} else {
		s.section("Relay")
		relay, token, err := s.chooseRelay()
		if err != nil {
			return err
		}
		if err := s.stopForReset(); err != nil {
			return err
		}
		s.section("This machine")
		if err := runInit(s.ctx, s.env, relay, token, s.o.name, s.o.reset); err != nil {
			return err
		}
	}
	st, err := s.daemonOnline(!configured, true)
	if err != nil {
		return err
	}
	if err := s.agents(); err != nil {
		return err
	}
	if err := s.offerPairing(st); err != nil {
		return err
	}
	s.done()
	return nil
}

// section starts a step of the wizard.
func (s *setup) section(title string) { fmt.Fprintf(s.w, "\n== %s ==\n", title) }

// agree asks a yes or no question; --yes answers yes.
func (s *setup) agree(question string, def bool) (bool, error) {
	if s.o.yes {
		return true, nil
	}
	return confirm(s.env.Prompt, question, def)
}

// confirm asks a yes or no question until it gets an answer; an empty
// answer is def.
func confirm(p Prompter, question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		a, err := p.Line(question+" ("+hint+")", "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// chooseRelay returns the relay origin and the admin token to init with:
// the --relay URL, one typed at the prompt, or a LAN test relay started on
// this machine.
func (s *setup) chooseRelay() (string, string, error) {
	relay := s.o.relay
	if relay == "" {
		if s.o.yes {
			return "", "", errors.New("setup --yes needs --relay <url> on a machine that is not set up yet")
		}
		fmt.Fprintln(s.w, "cravv-connect talks through a relay. For real use, deploy the Cloudflare relay (relay-cf/README.md);")
		fmt.Fprintln(s.w, "to try it on this network, this machine can run a test relay.")
		a, err := s.env.Prompt.Line("Relay URL (press Enter to start a LAN test relay here)", "")
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(a) == "" {
			return s.startLANRelay()
		}
		relay = strings.TrimSpace(a)
	}
	origin, err := relayOrigin(relay)
	if err != nil {
		return "", "", err
	}
	if err := relayaddr.Check(origin); err != nil {
		return "", "", fmt.Errorf("%w: other machines refuse join codes for it. Use https, or set it anyway with `cravv-connect init`", err)
	}
	if err := s.sys.RelayHealthy(s.ctx, origin); err != nil {
		return "", "", fmt.Errorf("the relay %s does not answer: %w", origin, err)
	}
	token := s.o.token
	if token == "" && !s.o.yes {
		token, err = s.env.Prompt.Password("Relay admin token (only for the relay's first machine; press Enter if another machine is already on it): ")
		if err != nil {
			return "", "", err
		}
	}
	return origin, strings.TrimSpace(token), nil
}

// daemonOnline makes sure the daemon runs and is connected to the relay.
// install installs it as a login service without asking (a fresh setup);
// otherwise a daemon that is not running is offered. needRelay waits for the
// relay connection.
func (s *setup) daemonOnline(install, needRelay bool) (ipc.StatusResult, error) {
	s.section("Daemon")
	if !daemonUp(s.ctx, s.env) {
		if !install {
			ok, err := s.agree("The daemon is not running. Install it as a login service and start it?", true)
			if err != nil {
				return ipc.StatusResult{}, err
			}
			if !ok {
				return ipc.StatusResult{}, errors.New("setup needs the daemon: run `cravv-connect daemon install` or `cravv-connect daemon run`")
			}
		}
		if err := s.run(newDaemonInstallCmd); err != nil {
			return ipc.StatusResult{}, err
		}
	} else {
		fmt.Fprintln(s.w, "Daemon is running.")
	}
	st, err := s.status()
	if err != nil || !needRelay {
		return st, err
	}
	return s.waitRelay(st, "If another machine is already on this relay, set this one up with "+
		"`cravv-connect setup --reset --join <code>` instead (a join code from `cravv-connect pair` there). ")
}

// waitRelay waits until the daemon is connected to the relay, polling from
// st; on a timeout the error names the daemon's errors, advice and the logs.
func (s *setup) waitRelay(st ipc.StatusResult, advice string) (ipc.StatusResult, error) {
	var err error
	if !st.RelayConnected {
		fmt.Fprintln(s.w, "Waiting for the relay connection...")
	}
	deadline := time.Now().Add(setupOnlineWait)
	for !st.RelayConnected {
		if time.Now().After(deadline) {
			msg := fmt.Sprintf("the daemon did not connect to the relay %s within %s", st.RelayURL, setupOnlineWait)
			for _, e := range st.Errors {
				msg += "; " + terminalSafe(e)
			}
			return st, fmt.Errorf("%s. %sLogs: %s", msg, advice, daemonLogHint(s.env))
		}
		select {
		case <-s.ctx.Done():
			return st, s.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if st, err = s.status(); err != nil {
			return st, err
		}
	}
	fmt.Fprintf(s.w, "Connected to the relay %s.\n", terminalSafe(st.RelayURL))
	return st, nil
}

func (s *setup) status() (ipc.StatusResult, error) {
	var st ipc.StatusResult
	err := withConn(s.ctx, s.env, func(c Caller) error { return c.Call(s.ctx, ipc.MethodStatus, nil, &st) })
	return st, err
}

// run runs another command's RunE (daemon install, daemon stop) as a step.
func (s *setup) run(f Factory) error {
	cmd := f(s.env)
	cmd.SetContext(s.ctx)
	return cmd.RunE(cmd, nil)
}

// installChecker is implemented by installers that can tell whether their
// integration is already in place; setup then skips them.
type installChecker interface{ Installed() bool }

// agents offers the integration for every detected agent.
func (s *setup) agents() error {
	s.section("Agents")
	if s.o.noAgents {
		fmt.Fprintln(s.w, "Skipped (--no-agents). Later: `cravv-connect install <agent>`.")
		return nil
	}
	if s.env.Agents == nil {
		fmt.Fprintln(s.w, "Agent integrations are not available in this build.")
		return nil
	}
	bin, err := s.env.Executable()
	if err != nil {
		return err
	}
	for _, name := range s.env.Agents.Names() {
		i, _ := s.env.Agents.Get(name)
		if !i.Detect() {
			fmt.Fprintf(s.w, "%s: not found on this machine, skipped.\n", name)
			continue
		}
		if c, ok := i.(installChecker); ok && c.Installed() {
			fmt.Fprintf(s.w, "%s: already set up.\n", name)
			continue
		}
		ok, err := s.agree(fmt.Sprintf("Add cravv-connect to %s?", name), true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(s.w, "%s: skipped. Later: `cravv-connect install %s`.\n", name, name)
			continue
		}
		if err := i.Install(s.ctx, bin); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(s.w, "Installed cravv-connect for %s. Restart it so it loads the MCP server.\n", name)
	}
	return nil
}

// offerPairing pairs a device when --pair is set, or when asked (the
// default is yes while there are no peers). --yes alone never pairs:
// pairing needs a person on the other machine.
func (s *setup) offerPairing(st ipc.StatusResult) error {
	s.section("Pair a device")
	if len(st.Peers) > 0 {
		names := make([]string, 0, len(st.Peers))
		for _, p := range st.Peers {
			names = append(names, terminalSafe(p.Alias))
		}
		fmt.Fprintf(s.w, "Paired with %s.\n", strings.Join(names, ", "))
	}
	pair := s.o.pair
	if !pair && !s.o.yes {
		var err error
		if pair, err = confirm(s.env.Prompt, "Pair a device now?", len(st.Peers) == 0); err != nil {
			return err
		}
	}
	if !pair {
		fmt.Fprintln(s.w, "Later: `cravv-connect pair` shows a join code for the other machine.")
		return nil
	}
	return withConn(s.ctx, s.env, func(c Caller) error { return pairNow(s.ctx, s.env, c, true) })
}

func (s *setup) done() {
	fmt.Fprintln(s.w, "\nSetup is complete. Restart your agents, then type /cravv in Claude Code to share a chat.")
}
