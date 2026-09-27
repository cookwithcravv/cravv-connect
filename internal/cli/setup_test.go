package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/install"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// serve starts the fake daemon and returns a function that stops it; the
// daemon can be served again afterwards (a restart).
func (fd *fakeDaemon) serve() (stop func()) {
	ln, err := ipc.Listen(fd.sock)
	if err != nil {
		fd.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { fd.srv.Serve(ctx, ln); close(done) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	fd.t.Cleanup(stop)
	return stop
}

// daemonProcess is the login service around a fake daemon: Install and
// Start serve it, Stop stops it. Every call is logged in events.
type daemonProcess struct {
	fd     *fakeDaemon
	mu     sync.Mutex
	stop   func()
	events []string
}

func (d *daemonProcess) log(e string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, e)
}

func (d *daemonProcess) up() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop == nil {
		d.stop = d.fd.serve()
	}
}

func (d *daemonProcess) Installed() bool { return true }
func (d *daemonProcess) Install(_ context.Context, bin string) error {
	d.log("install " + bin)
	d.up()
	return nil
}
func (d *daemonProcess) Uninstall(context.Context) error { d.log("uninstall"); return nil }
func (d *daemonProcess) Start(context.Context) error     { d.log("start"); d.up(); return nil }
func (d *daemonProcess) Stop(context.Context) error {
	d.log("stop")
	d.mu.Lock()
	stop := d.stop
	d.stop = nil
	d.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

type startedRelay struct {
	bin       string
	args, env []string
	logPath   string
}

// fakeSystem is SetupSystem without a network: names in resolves resolve,
// origins in healthy answer, and a started relay answers at its -origin.
type fakeSystem struct {
	mu       sync.Mutex
	resolves map[string][]string
	private  []netip.Addr
	healthy  map[string]bool
	startErr error
	started  []startedRelay
	lookups  []string
}

func (f *fakeSystem) LookupHost(_ context.Context, host string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, host)
	if a, ok := f.resolves[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func (f *fakeSystem) PrivateAddrs() ([]netip.Addr, error) { return f.private, nil }

func (f *fakeSystem) RelayHealthy(_ context.Context, origin string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.healthy[origin] {
		return nil
	}
	return errors.New("connection refused")
}

func (f *fakeSystem) StartRelay(bin string, args, env []string, logPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, startedRelay{bin, args, env, logPath})
	if i := slices.Index(args, "-origin"); i >= 0 {
		f.healthy[args[i+1]] = true
	}
	return nil
}

type setupRig struct {
	fd       *fakeDaemon
	env      *Env
	out      *bytes.Buffer
	errb     *bytes.Buffer
	prompt   *fakePrompter
	sys      *fakeSystem
	settings mapSettings
	daemon   *daemonProcess
	claude   *fakeInstaller
	codex    *fakeInstaller
}

// newSetupRig builds a machine for setup: a fake daemon that answers once
// its login service is installed or started, claude detected and codex not,
// and https://relay.example.com answering.
func newSetupRig(t *testing.T, st ipc.StatusResult) *setupRig {
	if st.Version == "" { // a daemon of this version (TestSetupRestartsAnotherVersion covers others)
		st.Version = BuildVersion()
	}
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, st)
	return newSetupRigOn(t, fd)
}

func newSetupRigOn(t *testing.T, fd *fakeDaemon) *setupRig {
	r := &setupRig{
		fd: fd, prompt: &fakePrompter{}, settings: mapSettings{},
		sys:    &fakeSystem{healthy: map[string]bool{"https://relay.example.com": true}},
		daemon: &daemonProcess{fd: fd},
		claude: &fakeInstaller{name: "claude", detected: true},
		codex:  &fakeInstaller{name: "codex"},
	}
	r.env, r.out, r.errb = fd.env(r.prompt, "")
	r.env.OpenSettings = func(string) (store.SettingsStore, func() error, error) {
		return r.settings, func() error { return nil }, nil
	}
	r.env.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }
	r.env.Service, r.env.ServiceSetup = r.daemon, r.daemon
	r.env.Agents = install.NewRegistry(r.claude, r.codex)
	r.env.Setup = r.sys
	return r
}

func (r *setupRig) run(args ...string) int {
	r.out.Reset()
	r.errb.Reset()
	return Main(append([]string{"setup"}, args...), r.env)
}

func (r *setupRig) config(t *testing.T) config.Config {
	t.Helper()
	paths, _ := r.env.Paths()
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func (r *setupRig) configure(t *testing.T, relay string) {
	t.Helper()
	paths, _ := r.env.Paths()
	cfg := config.Defaults()
	cfg.RelayURL, cfg.DeviceName = relay, "mac"
	if err := config.Save(paths, cfg); err != nil {
		t.Fatal(err)
	}
}

func (r *setupRig) notConfigured(t *testing.T) {
	t.Helper()
	paths, _ := r.env.Paths()
	if _, configured, _ := (&setup{paths: paths}).current(); configured {
		t.Fatal("setup wrote a configuration")
	}
}

var connected = ipc.StatusResult{RelayURL: "https://relay.example.com", RelayConnected: true}

// The first machine on a relay: a typed relay URL and admin token, init,
// the login service, the detected agent, and no pairing when declined.
func TestSetupFirstMachine(t *testing.T) {
	r := newSetupRig(t, connected)
	r.prompt.lines = []string{"https://relay.example.com", "", "n"}
	r.prompt.passwords = []string{"admin-tok"}
	if code := r.run(); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if cfg := r.config(t); cfg.RelayURL != "https://relay.example.com" || cfg.DeviceName != "prith-s-macbook" {
		t.Fatalf("config %+v", cfg)
	}
	if r.settings[SettingRelayAdminToken] != "admin-tok" {
		t.Fatalf("admin token %q", r.settings[SettingRelayAdminToken])
	}
	if !slices.Equal(r.daemon.events, []string{"install /usr/local/bin/cravv-connect"}) {
		t.Fatalf("service %v", r.daemon.events)
	}
	if r.claude.installed != "/usr/local/bin/cravv-connect" || r.codex.installed != "" {
		t.Fatalf("claude %q codex %q", r.claude.installed, r.codex.installed)
	}
	wantAsked := []string{
		"line: Relay URL (press Enter to start a LAN test relay here)",
		"password: Relay admin token (only for the relay's first machine; press Enter if another machine is already on it): ",
		"line: Add cravv-connect to claude? (Y/n)",
		"line: Pair a device now? (Y/n)",
	}
	if !slices.Equal(r.prompt.asked, wantAsked) {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	for _, want := range []string{
		"\n== Relay ==\n", "\n== This machine ==\nInitialized cravv-connect in ",
		"\n== Daemon ==\nDaemon installed as a login service and started.\n",
		"Connected to the relay https://relay.example.com.\n",
		"\n== Agents ==\n", "Installed cravv-connect for claude. Restart it so it loads the MCP server.\n",
		"codex: not found on this machine, skipped.\n",
		"\n== Pair a device ==\nLater: `cravv-connect pair` shows a join code for the other machine.\n",
		"\nSetup is complete. Restart your agents, then type /cravv in Claude Code to share a chat.\n",
	} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.out.String())
		}
	}
	if strings.Contains(r.out.String(), "Next: run `cravv-connect daemon install`") {
		t.Error("setup printed init's next step")
	}
}

// --yes asks nothing: the relay comes from --relay, every detected agent is
// set up, and nobody is paired.
func TestSetupYesAsksNothing(t *testing.T) {
	r := newSetupRig(t, connected)
	if code := r.run("--yes", "--relay", "https://relay.example.com", "--relay-token", "tok", "--name", "Build Box"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if len(r.prompt.asked) != 0 {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	if cfg := r.config(t); cfg.DeviceName != "build-box" || r.settings[SettingRelayAdminToken] != "tok" || r.claude.installed == "" {
		t.Fatalf("config %+v token %q claude %q", cfg, r.settings[SettingRelayAdminToken], r.claude.installed)
	}
}

// A relay setup cannot use is refused before anything is written.
func TestSetupRelayChecks(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--yes"}, "error: setup --yes needs --relay <url> on a machine that is not set up yet\n"},
		{[]string{"--relay", "http://relay.example.com"}, "error: invalid relay URL: plain http is only allowed for localhost or a private network address: " +
			"other machines refuse join codes for it. Use https, or set it anyway with `cravv-connect init`\n"},
		{[]string{"--relay", "https://down.example.com"}, "error: the relay https://down.example.com does not answer: connection refused\n"},
		{[]string{"--relay", "https://relay.example.com/v1"}, "error: relay must be scheme://host[:port] with no path, query or user: "},
	} {
		r := newSetupRig(t, connected)
		if code := r.run(tc.args...); code != 1 || !strings.HasPrefix(r.errb.String(), tc.want) {
			t.Errorf("%v: code %d stderr %q", tc.args, code, r.errb.String())
		}
		r.notConfigured(t)
		if len(r.daemon.events) != 0 {
			t.Errorf("%v: service %v", tc.args, r.daemon.events)
		}
	}
}

// An empty relay answer starts cravv-relay from next to this binary on
// <host>.local, with the admin token only in its environment.
func TestSetupStartsLANRelay(t *testing.T) {
	r := newSetupRig(t, ipc.StatusResult{RelayURL: "http://prithvis-mac.local:8787", RelayConnected: true})
	r.env.Hostname = func() (string, error) { return "Prithvis-Mac.lan", nil }
	r.sys.resolves = map[string][]string{"prithvis-mac.local": {"192.168.1.10"}}
	r.prompt.lines = []string{"", "n"}
	if code := r.run("--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if len(r.sys.started) != 1 {
		t.Fatalf("started %v", r.sys.started)
	}
	s := r.sys.started[0]
	if s.bin != "/usr/local/bin/cravv-relay" || !slices.Equal(s.args, []string{"-addr", "0.0.0.0:8787", "-origin", "http://prithvis-mac.local:8787"}) {
		t.Fatalf("started %s %q", s.bin, s.args)
	}
	token, ok := strings.CutPrefix(strings.Join(s.env, ""), "CRAVV_RELAY_ADMIN_TOKEN=")
	if !ok || len(s.env) != 1 || len(token) != 43 {
		t.Fatalf("relay env %q", s.env)
	}
	if r.settings[SettingRelayAdminToken] != token {
		t.Fatalf("stored admin token %q, relay has %q", r.settings[SettingRelayAdminToken], token)
	}
	if cfg := r.config(t); cfg.RelayURL != "http://prithvis-mac.local:8787" {
		t.Fatalf("relay %q", cfg.RelayURL)
	}
	if !strings.HasSuffix(s.logPath, "/relay.log") || !strings.Contains(r.out.String(), "Started a LAN test relay at http://prithvis-mac.local:8787 (log: ") {
		t.Fatalf("log %s\n%s", s.logPath, r.out.String())
	}
	if slices.Contains(r.prompt.asked, "password: Relay admin token (only for the relay's first machine; press Enter if another machine is already on it): ") {
		t.Fatal("asked for the admin token of the relay it started")
	}
	if !strings.Contains(r.out.String(), "This test relay is reachable by anyone on this network. Use it only on a trusted network.\n") {
		t.Fatalf("no network warning:\n%s", r.out.String())
	}
}

// With no network address the LAN test relay listens on loopback only, says
// other machines cannot reach it, and setup does not offer a join code
// another machine could not use.
func TestSetupLoopbackLANRelay(t *testing.T) {
	r := newSetupRig(t, ipc.StatusResult{RelayURL: "http://127.0.0.1:8787", RelayConnected: true})
	r.env.Hostname = func() (string, error) { return "gpu-box", nil }
	r.prompt.lines = []string{""}
	if code := r.run("--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if len(r.sys.started) != 1 || !slices.Equal(r.sys.started[0].args, []string{"-addr", "127.0.0.1:8787", "-origin", "http://127.0.0.1:8787"}) {
		t.Fatalf("started %v", r.sys.started)
	}
	out := r.out.String()
	for _, want := range []string{
		"No network address was found, so the test relay listens on this machine only: other machines cannot reach it.\n",
		"== Pair a device ==\nThe relay http://127.0.0.1:8787 is reachable only from this machine, so another machine could not use a join code for it. " +
			"To pair with another machine, use a relay it can reach: `cravv-connect setup --reset`.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "reachable by anyone on this network") {
		t.Error("warned about the network for a loopback relay")
	}
	if len(r.prompt.asked) != 1 {
		t.Fatalf("asked %q", r.prompt.asked)
	}
}

// <host>.local when it resolves, else the first private IPv4 address, else
// loopback; a host name that is not a DNS label is never looked up.
func TestLANRelayHost(t *testing.T) {
	v4, v6 := netip.MustParseAddr("192.168.1.23"), netip.MustParseAddr("fd00::1")
	for _, tc := range []struct {
		host     string
		resolves map[string][]string
		private  []netip.Addr
		want     string
	}{
		{"Mac.lan", map[string][]string{"mac.local": {"192.168.1.23"}}, []netip.Addr{v4}, "mac.local"},
		{"gpu-box", nil, []netip.Addr{v6, v4}, "192.168.1.23"},
		{"Prith's MacBook", map[string][]string{"prith's macbook.local": {"x"}}, []netip.Addr{v6}, "fd00::1"},
		{"gpu-box", nil, nil, "127.0.0.1"},
	} {
		sys := &fakeSystem{resolves: tc.resolves, private: tc.private}
		s := &setup{ctx: context.Background(), sys: sys, env: &Env{Hostname: func() (string, error) { return tc.host, nil }}}
		if got := s.lanHost(); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.host, got, tc.want)
		}
		for _, l := range sys.lookups {
			if strings.ContainsAny(l, "' ") {
				t.Errorf("%s: looked up %q", tc.host, l)
			}
		}
	}
}

func TestSetupLANRelayProblems(t *testing.T) {
	r := newSetupRig(t, connected)
	r.env.Hostname = func() (string, error) { return "mac", nil }
	r.sys.resolves = map[string][]string{"mac.local": {"192.168.1.10"}}
	r.sys.startErr = &fs.PathError{Op: "fork/exec", Path: "/usr/local/bin/cravv-relay", Err: fs.ErrNotExist}
	r.prompt.lines = []string{""}
	if code := r.run(); code != 1 || r.errb.String() != "error: cravv-relay was not found next to cravv-connect (/usr/local/bin/cravv-relay); install it from the release archive, or enter a relay URL\n" {
		t.Fatalf("missing binary: %d %q", code, r.errb.String())
	}
	r.notConfigured(t)

	r2 := newSetupRig(t, connected)
	r2.env.Hostname = func() (string, error) { return "mac", nil }
	r2.sys.resolves = map[string][]string{"mac.local": {"192.168.1.10"}}
	r2.sys.healthy["http://mac.local:8787"] = true
	r2.prompt.lines = []string{""}
	if code := r2.run(); code != 1 || !strings.Contains(r2.errb.String(), "a relay already answers at http://mac.local:8787. If another machine is on it, set this one up with `cravv-connect setup --join <code>`") || len(r2.sys.started) != 0 {
		t.Fatalf("already running: %d %q", code, r2.errb.String())
	}
}

// Running setup again shows what is set up and offers only what is missing:
// no init, the daemon when it is not running, the agents, and pairing
// (default no once there are peers).
func TestSetupRerunOffersMissingSteps(t *testing.T) {
	st := connected
	st.Peers = []ipc.PeerView{{Alias: "gpu-box"}}
	r := newSetupRig(t, st)
	r.configure(t, "https://relay.example.com")
	r.prompt.lines = []string{"", "n", ""}
	if code := r.run(); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	out := r.out.String()
	if !strings.HasPrefix(out, "This machine is set up for relay https://relay.example.com as mac.\n") ||
		!strings.Contains(out, "Paired with gpu-box.\n") || !strings.Contains(out, "claude: skipped. Later: `cravv-connect install claude`.\n") {
		t.Fatalf("stdout\n%s", out)
	}
	wantAsked := []string{
		"line: The daemon is not running. Install it as a login service and start it? (Y/n)",
		"line: Add cravv-connect to claude? (Y/n)",
		"line: Pair a device now? (y/N)",
	}
	if !slices.Equal(r.prompt.asked, wantAsked) || len(r.daemon.events) != 1 || r.claude.installed != "" {
		t.Fatalf("asked %q service %v", r.prompt.asked, r.daemon.events)
	}
	if r.config(t).DeviceName != "mac" {
		t.Fatal("setup ran init again")
	}

	// Once the daemon runs, it is only reported.
	r.prompt.lines, r.prompt.asked = []string{"n"}, nil
	if code := r.run("--no-agents"); code != 0 || !strings.Contains(r.out.String(), "== Daemon ==\nDaemon is running.\nConnected to the relay") || len(r.daemon.events) != 1 {
		t.Fatalf("second run: %d %s %v", code, r.out.String(), r.daemon.events)
	}
}

// A machine set up for one relay is never moved to another without --reset.
func TestSetupRefusesAnotherRelayWithoutReset(t *testing.T) {
	r := newSetupRig(t, connected)
	r.configure(t, "https://relay.example.com")
	r.sys.healthy["https://other.example.com"] = true
	if code := r.run("--relay", "https://other.example.com"); code != 1 || r.errb.String() != "error: this machine is set up for relay https://relay.example.com; "+
		"to move it to https://other.example.com, run `cravv-connect setup --reset --relay https://other.example.com` (you pair again with every peer)\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if r.config(t).RelayURL != "https://relay.example.com" || len(r.daemon.events) != 0 {
		t.Fatal("setup changed the machine")
	}
}

// --reset asks first; yes stops the daemon, runs init again (clearing the
// old relay's registration) and installs the daemon again.
func TestSetupResetAsksFirst(t *testing.T) {
	r := newSetupRig(t, ipc.StatusResult{RelayURL: "https://other.example.com", RelayConnected: true})
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	r.sys.healthy["https://other.example.com"] = true
	r.prompt.lines = []string{"n"}
	if code := r.run("--reset", "--relay", "https://other.example.com", "--no-agents"); code != 0 || r.out.String() != "Nothing changed.\n" {
		t.Fatalf("declined: %d %q", code, r.out.String())
	}
	if r.config(t).RelayURL != "https://relay.example.com" || len(r.daemon.events) != 0 {
		t.Fatal("a declined reset changed the machine")
	}
	if r.prompt.asked[0] != "line: Set this machine up again? It is set up for relay https://relay.example.com; on a new relay you pair again with every peer. (y/N)" {
		t.Fatalf("asked %q", r.prompt.asked)
	}

	r.prompt.lines, r.prompt.passwords = []string{"y", "n"}, []string{""}
	r.settings[settingRelayRegistered] = "1"
	if code := r.run("--reset", "--relay", "https://other.example.com", "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if r.config(t).RelayURL != "https://other.example.com" || r.settings[settingRelayRegistered] != "" {
		t.Fatalf("config %+v settings %v", r.config(t), r.settings)
	}
	if !slices.Equal(r.daemon.events, []string{"stop", "install /usr/local/bin/cravv-connect"}) {
		t.Fatalf("service %v", r.daemon.events)
	}
}

// Setup waits for the relay connection, and when it does not come says why
// and what to do instead.
func TestSetupWaitsForTheRelay(t *testing.T) {
	defer func(d time.Duration) { setupOnlineWait = d }(setupOnlineWait)
	setupOnlineWait = 10 * time.Second
	fd := newFakeDaemon(t)
	var mu sync.Mutex
	calls := 0
	fd.handle(ipc.MethodStatus, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return ipc.StatusResult{RelayURL: "https://relay.example.com", RelayConnected: calls > 4}, nil
	})
	r := newSetupRigOn(t, fd)
	if code := r.run("--yes", "--relay", "https://relay.example.com", "--relay-token", "tok"); code != 0 ||
		!strings.Contains(r.out.String(), "Waiting for the relay connection...\nConnected to the relay https://relay.example.com.\n") {
		t.Fatalf("code %d stdout %s stderr %s", code, r.out.String(), r.errb.String())
	}

	setupOnlineWait = 300 * time.Millisecond
	fd2 := newFakeDaemon(t)
	fd2.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{RelayURL: "https://relay.example.com", Errors: []string{"relay: registration refused"}})
	r2 := newSetupRigOn(t, fd2)
	if code := r2.run("--yes", "--relay", "https://relay.example.com"); code != 1 ||
		!strings.Contains(r2.errb.String(), "error: the daemon did not connect to the relay https://relay.example.com within 300ms; relay: registration refused. "+
			"If another machine is already on this relay, set this one up with `cravv-connect setup --reset --join <code>` instead") {
		t.Fatalf("code %d stderr %q", code, r2.errb.String())
	}
	if r2.claude.installed != "" {
		t.Fatal("went on to the agents without a relay connection")
	}
}

// --pair pairs without asking and shows the join code.
func TestSetupPairFlag(t *testing.T) {
	fd := pairDaemonUnstarted(t, "https://relay.example.com")
	r := newSetupRigOn(t, fd)
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	r.prompt.passwords, r.prompt.lines = []string{"pw"}, []string{""}
	if code := r.run("--pair", "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if !strings.Contains(r.out.String(), "== Pair a device ==\nJoin code: "+testJoinCode+"\n") || !strings.Contains(r.out.String(), "Paired with gpu-box.") {
		t.Fatalf("stdout\n%s", r.out.String())
	}
	if slices.Contains(r.prompt.asked, "line: Pair a device now? (Y/n)") {
		t.Fatal("asked although --pair was given")
	}
}

// The real system: a missing relay binary is fs.ErrNotExist (setup turns it
// into advice), and only a cravv relay's health answer counts as healthy.
func TestRealSetupSystem(t *testing.T) {
	dir := t.TempDir()
	err := realSystem{}.StartRelay(filepath.Join(dir, "cravv-relay"), nil, nil, filepath.Join(dir, "relay.log"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing binary: %v", err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != relayproto.PathHealth {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"ok":true,"version":1}`))
	}))
	defer relay.Close()
	if err := (realSystem{}).RelayHealthy(context.Background(), relay.URL); err != nil {
		t.Fatalf("relay: %v", err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) }))
	defer other.Close()
	if err := (realSystem{}).RelayHealthy(context.Background(), other.URL); err == nil {
		t.Fatal("a web server passed for a relay")
	}
}

// --reset keeps the daemon running until the new relay is known to answer:
// a relay that does not answer changes nothing.
func TestSetupResetUnreachableRelayKeepsDaemon(t *testing.T) {
	r := newSetupRig(t, connected)
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	r.prompt.lines, r.prompt.passwords = []string{"y"}, []string{""}
	if code := r.run("--reset", "--relay", "https://down.example.com", "--no-agents"); code != 1 ||
		r.errb.String() != "error: the relay https://down.example.com does not answer: connection refused. "+
			"Nothing changed: this machine still uses relay https://relay.example.com\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if len(r.daemon.events) != 0 || !daemonUp(context.Background(), r.env) {
		t.Fatalf("service %v", r.daemon.events)
	}
	if r.config(t).RelayURL != "https://relay.example.com" {
		t.Fatal("config changed")
	}
}

// When setup fails after it stopped the daemon for a reset, it starts the
// daemon again and says so.
func TestSetupResetRestartsDaemonAfterFailure(t *testing.T) {
	r := newSetupRig(t, connected)
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	r.sys.healthy["https://other.example.com"] = true
	r.env.OpenSettings = func(string) (store.SettingsStore, func() error, error) {
		return nil, nil, errors.New("database is locked")
	}
	r.prompt.lines, r.prompt.passwords = []string{"y"}, []string{""}
	if code := r.run("--reset", "--relay", "https://other.example.com", "--no-agents"); code != 1 || r.errb.String() != "error: database is locked\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if !slices.Equal(r.daemon.events, []string{"stop", "start"}) || !daemonUp(context.Background(), r.env) {
		t.Fatalf("service %v", r.daemon.events)
	}
	if !strings.Contains(r.out.String(), "Setup did not finish, so the daemon was started again.\n") {
		t.Fatalf("stdout\n%s", r.out.String())
	}
}

// --relay-token only matters for a machine's first registration.
func TestSetupRelayTokenOnSetUpMachine(t *testing.T) {
	r := newSetupRig(t, connected)
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	if code := r.run("--yes", "--relay-token", "tok", "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if r.errb.String() != "Warning: --relay-token ignored: this machine is already registered.\n" {
		t.Fatalf("stderr %q", r.errb.String())
	}
	if _, ok := r.settings[SettingRelayAdminToken]; ok {
		t.Fatal("stored the token")
	}
}

// After a restart the LAN test relay this machine ran is gone: setup advises
// starting a new one rather than joining another machine.
func TestSetupAdvisesNewLANRelayAfterRestart(t *testing.T) {
	defer func(d time.Duration) { setupOnlineWait = d }(setupOnlineWait)
	setupOnlineWait = 300 * time.Millisecond
	for _, relay := range []string{"http://mac.local:8787", "http://192.168.1.10:8787", "http://127.0.0.1:8787"} {
		r := newSetupRig(t, ipc.StatusResult{RelayURL: relay, Errors: []string{"relay: connection refused"}})
		r.env.Hostname = func() (string, error) { return "Mac.lan", nil }
		r.sys.private = []netip.Addr{netip.MustParseAddr("192.168.1.10")}
		r.configure(t, relay)
		r.daemon.up()
		if code := r.run("--no-agents"); code != 1 {
			t.Fatalf("%s: code %d", relay, code)
		}
		want := "; relay: connection refused. The relay " + relay + " is on this machine, and a LAN test relay stops when the machine restarts: " +
			"start a new one with `cravv-connect setup --reset` (you pair again with every peer). Logs: "
		if !strings.Contains(r.errb.String(), want) || strings.Contains(r.errb.String(), "--join") {
			t.Errorf("%s: stderr %q", relay, r.errb.String())
		}
	}

	r := newSetupRig(t, ipc.StatusResult{RelayURL: "http://gpu-box.local:8787"})
	r.env.Hostname = func() (string, error) { return "Mac.lan", nil }
	r.sys.private = []netip.Addr{netip.MustParseAddr("192.168.1.10")}
	r.configure(t, "http://gpu-box.local:8787")
	r.daemon.up()
	if code := r.run("--no-agents"); code != 1 || !strings.Contains(r.errb.String(), "`cravv-connect setup --reset --join <code>`") {
		t.Errorf("another machine's relay: %d %q", code, r.errb.String())
	}
}

// checkedInstaller is an installer that can tell whether its integration
// is in place (current) or older, as the Claude Code installer does.
type checkedInstaller struct {
	fakeInstaller
	current, older bool
}

func (c *checkedInstaller) Installed() bool { return c.current }
func (c *checkedInstaller) Outdated() bool  { return c.older }

// setup never asks again about an agent that is set up, and updates an
// older integration without asking.
func TestSetupSkipsCurrentAgentsAndUpdatesOlderOnes(t *testing.T) {
	for _, tc := range []struct {
		current, older bool
		want           string
		installs       bool
	}{
		{true, false, "claude: already set up.\n", false},
		{false, true, "Updated cravv-connect for claude to this version. Restart it so it loads the new version.\n", true},
	} {
		r := newSetupRig(t, connected)
		r.configure(t, "https://relay.example.com")
		r.daemon.up()
		claude := &checkedInstaller{fakeInstaller: fakeInstaller{name: "claude", detected: true}, current: tc.current, older: tc.older}
		r.env.Agents = install.NewRegistry(claude, r.codex)
		r.prompt.lines = []string{"n"} // pairing
		if code := r.run(); code != 0 {
			t.Fatalf("code %d stderr %s", code, r.errb.String())
		}
		if !strings.Contains(r.out.String(), tc.want) || (claude.installed != "") != tc.installs {
			t.Fatalf("%+v: installed %q\n%s", tc, claude.installed, r.out.String())
		}
		if !slices.Equal(r.prompt.asked, []string{"line: Pair a device now? (y/N)"}) && !slices.Equal(r.prompt.asked, []string{"line: Pair a device now? (Y/n)"}) {
			t.Fatalf("%+v: asked %q", tc, r.prompt.asked)
		}
	}
}
