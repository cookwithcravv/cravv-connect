package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/auth"
	"github.com/cookwithcravv/cravv-connect/internal/cli"
	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/daemon"
	"github.com/cookwithcravv/cravv-connect/internal/install"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/store/sqlite"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// installedBinary is where the fresh machines' cravv-connect is installed
// (install.sh puts it in ~/.local/bin; any path outside the temporary
// folder does, since setup refuses to install a temporary build).
const installedBinary = "/home/tester/.local/bin/cravv-connect"

// syncBuffer is a bytes.Buffer the test can read while a command writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// promptStep is one question the person at a fresh machine answers: the
// prompt must contain ask, and answer is typed.
type promptStep struct {
	password bool
	ask      string
	answer   string
}

func askLine(ask, answer string) promptStep { return promptStep{ask: ask, answer: answer} }
func askPassword(ask, answer string) promptStep {
	return promptStep{password: true, ask: ask, answer: answer}
}

// scriptPrompter answers setup's questions in order, and fails the answer
// (so setup stops) when a question is not the one expected next.
type scriptPrompter struct {
	mu    sync.Mutex
	steps []promptStep
	asked []string
}

func (p *scriptPrompter) next(password bool, prompt, def string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, prompt)
	if len(p.steps) == 0 {
		return "", fmt.Errorf("unexpected question %q", prompt)
	}
	s := p.steps[0]
	if s.password != password || !strings.Contains(prompt, s.ask) {
		return "", fmt.Errorf("asked %q, want a question about %q", prompt, s.ask)
	}
	p.steps = p.steps[1:]
	if s.answer == "" {
		return def, nil
	}
	return s.answer, nil
}

func (p *scriptPrompter) Password(prompt string) (string, error) { return p.next(true, prompt, "") }
func (p *scriptPrompter) Line(prompt, def string) (string, error) {
	return p.next(false, prompt, def)
}

// left returns the questions not asked yet.
func (p *scriptPrompter) left() []promptStep {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]promptStep(nil), p.steps...)
}

// claudeCLI stands in for the claude command the installer runs:
// `claude mcp add --scope user` records the server in ~/.claude.json, as
// the real CLI does.
type claudeCLI struct {
	home string
	mu   sync.Mutex
	runs []string
}

func (c *claudeCLI) Run(_ context.Context, name string, args ...string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs = append(c.runs, name+" "+strings.Join(args, " "))
	if len(args) > 1 && args[0] == "mcp" && args[1] == "add" {
		cfg := `{"mcpServers": {"cravv-connect": {"type": "stdio", "command": "` + args[len(args)-2] + `", "args": ["mcp"]}}}`
		return "", os.WriteFile(filepath.Join(c.home, ".claude.json"), []byte(cfg), 0o600)
	}
	return "", nil
}

// lanSystem is SetupSystem for the test relay: no name resolves and no
// relay is started; the health check goes to the in-process relay.
type lanSystem struct{ relay *Relay }

func (lanSystem) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("no such host")
}
func (lanSystem) PrivateAddrs() ([]netip.Addr, error) { return nil, nil }
func (s lanSystem) RelayHealthy(ctx context.Context, origin string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+relayproto.PathHealth, nil)
	if err != nil {
		return err
	}
	res, err := s.relay.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var h relayproto.Health
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&h) != nil || !h.OK {
		return errors.New("not a cravv relay")
	}
	return nil
}
func (lanSystem) StartRelay(string, []string, []string, string) error {
	return errors.New("no LAN relay in this test")
}

// freshMachine is a machine with nothing set up, for the real
// `cravv-connect setup`: an empty state folder, a home folder where Claude
// Code keeps its settings, and a login service that, when setup installs
// it, starts the real daemon in this process on the relay (instead of
// launchd or systemd). Only the password verifier, the identity store and
// the desktop differ from production, as for every e2e node.
type freshMachine struct {
	t      *testing.T
	name   string
	relay  *Relay
	paths  config.Paths
	home   string
	prompt *scriptPrompter
	out    *syncBuffer
	claude *claudeCLI

	mu   sync.Mutex
	node *Node // the daemon, once the service started it
}

func newFreshMachine(t *testing.T, r *Relay, name string) *freshMachine {
	t.Helper()
	dir, err := os.MkdirTemp("", "ccs-") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	state, home := filepath.Join(dir, "s"), filepath.Join(dir, "home")
	for _, d := range []string{state, filepath.Join(home, ".claude"), filepath.Join(home, "work", "proj")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &freshMachine{
		t: t, name: name, relay: r, home: home, prompt: &scriptPrompter{}, out: &syncBuffer{}, claude: &claudeCLI{home: home},
		paths: config.Paths{
			Home: state, Config: filepath.Join(state, "config.toml"), DB: filepath.Join(state, "store.db"),
			Audit: filepath.Join(state, "audit.log"), Socket: filepath.Join(state, "d.sock"),
			Files: filepath.Join(state, "files"), Log: filepath.Join(state, "daemon.log"),
		},
	}
}

// Installed, Start and Stop make the machine its own service manager.
func (m *freshMachine) Installed() bool { return true }
func (m *freshMachine) Start(ctx context.Context) error {
	return m.Install(ctx, installedBinary)
}
func (m *freshMachine) Stop(context.Context) error {
	m.mu.Lock()
	n := m.node
	m.node = nil
	m.mu.Unlock()
	if n != nil {
		n.Stop()
	}
	return nil
}
func (m *freshMachine) Uninstall(ctx context.Context) error { return m.Stop(ctx) }

// Install starts the daemon from what setup wrote (config.toml, store.db).
func (m *freshMachine) Install(_ context.Context, bin string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.node != nil {
		return nil
	}
	if bin != installedBinary {
		return fmt.Errorf("service for %q, want %q", bin, installedBinary)
	}
	cfg, err := config.Load(m.paths)
	if err != nil {
		return err
	}
	rc, err := relayclient.New(cfg.RelayURL, relayclient.WithHTTPClient(m.relay.HTTPClient()))
	if err != nil {
		return err
	}
	desk := &Desktop{}
	opts := daemon.Options{
		Paths: m.paths, Config: cfg, Version: cli.BuildVersion(), Clock: core.SystemClock{}, Relay: rc,
		Verifier: auth.Fake{Password: Password}, Desktop: desk, Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore {
			return daemon.SettingsIdentityStore{Settings: s}
		},
		ReconnectMin: 50 * time.Millisecond, MaintenanceEvery: time.Hour, FileRetryDelay: 100 * time.Millisecond,
	}
	d, err := daemon.New(opts)
	if err != nil {
		return err
	}
	n := &Node{t: m.t, Name: m.name, Dir: m.paths.Home, Proj: filepath.Join(m.home, "work", "proj"), Paths: m.paths,
		Daemon: d, Clock: core.SystemClock{}, Desktop: desk, opts: opts}
	n.start()
	m.node = n
	return nil
}

// Node returns the machine's daemon once setup started it.
func (m *freshMachine) Node() *Node {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.node == nil {
		m.t.Fatalf("%s: setup did not start the daemon", m.name)
	}
	return m.node
}

// env is the command line's environment on this machine: its folders,
// terminal (the script), agents (Claude Code found, Codex not) and the
// service manager above.
func (m *freshMachine) env() *cli.Env {
	agents := install.NewRegistry(
		&install.Claude{Home: m.home, Run: m.claude, LookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/home/tester/.local/bin/claude", nil
			}
			return "", errors.New("not found")
		}},
		&install.Codex{Home: m.home, LookPath: func(string) (string, error) { return "", errors.New("not found") }},
	)
	return &cli.Env{
		Stdin: strings.NewReader(""), Stdout: m.out, Stderr: m.out, Prompt: m.prompt,
		Paths:    func() (config.Paths, error) { return m.paths, nil },
		Dial:     func(ctx context.Context) (cli.Caller, error) { return ipc.DialContext(ctx, m.paths.Socket) },
		Getwd:    func() (string, error) { return m.home, nil },
		Hostname: func() (string, error) { return m.name + ".lan", nil },
		OpenSettings: func(dbPath string) (store.SettingsStore, func() error, error) {
			db, err := sqlite.Open(dbPath)
			if err != nil {
				return nil, nil, err
			}
			return db, db.Close, nil
		},
		Executable: func() (string, error) { return installedBinary, nil },
		Service:    m, ServiceSetup: m, Agents: agents, Setup: lanSystem{m.relay},
	}
}

// Run runs `cravv-connect args...` on this machine and returns its exit
// code; the output accumulates in m.out.
func (m *freshMachine) Run(args ...string) int { return cli.Main(args, m.env()) }

var joinCodeLine = regexp.MustCompile(`cravv-connect setup --join (cravv-join:\S+)`)

// JoinCode waits for the join code setup shows on this machine.
func (m *freshMachine) JoinCode() string {
	m.t.Helper()
	var code string
	Eventually(m.t, wait, m.name+" shows a join code", func() bool {
		if s := joinCodeLine.FindStringSubmatch(m.out.String()); s != nil {
			code = s[1]
		}
		return code != ""
	})
	return code
}

// TestAcceptance_6_Setup pins criterion 6, "Setup": "A fresh machine goes
// from nothing to paired and chat-connected with an install one-liner plus
// one command (`cravv-connect setup`, or `setup --join <code>`)." and "No
// Go toolchain is needed."
//
// Two fresh machines with empty state and home folders run the real setup
// wizard against a real relay: the Mac hosts (relay, admin token, daemon,
// Claude Code, pair a device now) and the GPU box joins with the code the
// Mac shows. Nothing but the one command runs on either machine. The
// install one-liner and the release binaries (no Go toolchain) are pinned
// by scripts/install_test.go and cmd/cravv-connect/release_test.go.
func TestAcceptance_6_Setup(t *testing.T) {
	t.Parallel()
	r := NewLANRelay(t)
	mac, gpu := newFreshMachine(t, r, "mac"), newFreshMachine(t, r, "gpu-box")

	mac.prompt.steps = []promptStep{
		askLine("Relay URL", r.URL()),
		askPassword("Relay admin token", AdminToken),
		askLine("Add cravv-connect to claude?", ""),
		askLine("Pair a device now?", ""),
		askPassword("Login password", Password),
		askLine("Local name for this peer", ""),
	}
	gpu.prompt.steps = []promptStep{
		askLine("Join relay "+r.URL()+"?", "y"),
		askPassword("Login password", Password),
		askLine("Local name for this peer", ""),
		askLine("Add cravv-connect to claude?", "y"),
	}
	macDone := make(chan int, 1)
	go func() { macDone <- mac.Run("setup") }()
	code := mac.JoinCode()
	if got := gpu.Run("setup", "--join", code); got != 0 {
		t.Fatalf("setup --join exited %d:\n%s", got, gpu.out.String())
	}
	select {
	case got := <-macDone:
		if got != 0 {
			t.Fatalf("setup exited %d:\n%s", got, mac.out.String())
		}
	case <-time.After(wait):
		t.Fatalf("setup on the Mac did not finish:\n%s", mac.out.String())
	}

	for _, m := range []*freshMachine{mac, gpu} {
		out := m.out.String()
		if left := m.prompt.left(); len(left) != 0 {
			t.Errorf("%s never asked %+v; it asked %q", m.name, left, m.prompt.asked)
		}
		for _, want := range []string{"Setup is complete.", "Installed cravv-connect for claude.", "codex: not found on this machine, skipped."} {
			if !strings.Contains(out, want) {
				t.Errorf("%s's setup output lacks %q:\n%s", m.name, want, out)
			}
		}
		if strings.Contains(out, "\u2014") {
			t.Errorf("%s's setup output has an em dash", m.name)
		}
		// Claude Code has the MCP server, the hooks, the allow rules and
		// the /cravv skill.
		if want := "claude mcp add --scope user cravv-connect -- " + installedBinary + " mcp"; len(m.claude.runs) != 1 || m.claude.runs[0] != want {
			t.Errorf("%s ran %q, want %q", m.name, m.claude.runs, want)
		}
		settings, err := os.ReadFile(filepath.Join(m.home, ".claude", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{installedBinary + " hook", `"mcp__cravv-connect__check_inbox"`, `"Bash(cravv-connect listen:*)"`} {
			if !strings.Contains(string(settings), want) {
				t.Errorf("%s's Claude settings lack %s:\n%s", m.name, want, settings)
			}
		}
		if _, err := os.Stat(filepath.Join(m.home, ".claude", "skills", "cravv", "SKILL.md")); err != nil {
			t.Errorf("%s has no /cravv skill: %v", m.name, err)
		}
	}

	// Paired: each daemon is on the relay and knows the other by name.
	macNode, gpuNode := mac.Node(), gpu.Node()
	for _, c := range []struct {
		n    *Node
		peer string
	}{{macNode, "gpu-box"}, {gpuNode, "mac"}} {
		st := c.n.Status()
		if !st.RelayConnected || len(st.Peers) != 1 || st.Peers[0].Alias != c.peer {
			t.Fatalf("%s after setup: %+v", c.n.Name, st)
		}
	}

	// Chat-connected: a chat on each machine, through the MCP server setup
	// registered, shares, links and talks.
	human := &Human{}
	trainer, _ := newClaudeAgent(t, gpuNode, "chat-trainer", human)
	lead, _ := newClaudeAgent(t, macNode, "chat-lead", nil)
	shareChat(t, trainer, "trainer", "all-peers")
	shareChat(t, lead, "lead", "private")
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "gpu-box/trainer", "permission": "messages"}, &out)
	gpuNode.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
	human.Answer(Choose("accept"))
	trainer.call("review_pending", nil)
	macNode.WaitLink(wait, "linked", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
	lead.call("send_message", map[string]any{"link": out.Link, "text": "ACC6-HELLO"})
	Eventually(t, wait, "the message in the GPU box's chat", func() bool {
		return strings.Contains(trainer.call("check_inbox", nil), "ACC6-HELLO")
	})

	// Setup again only reports: nothing is asked about Claude Code, and
	// nothing is installed twice.
	mac.out.Reset()
	mac.prompt.steps = []promptStep{askLine("Pair a device now?", "n")}
	if got := mac.Run("setup"); got != 0 {
		t.Fatalf("setup again exited %d:\n%s", got, mac.out.String())
	}
	if out := mac.out.String(); !strings.Contains(out, "claude: already set up.") || len(mac.claude.runs) != 1 {
		t.Fatalf("setup again: runs %q\n%s", mac.claude.runs, out)
	}
}
