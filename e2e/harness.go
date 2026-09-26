// Package e2e runs whole cravv-connect machines against an in-process relay:
// a real relayserver over HTTP, real daemons with SQLite stores in temp
// directories, the real relay client, and the real IPC API on unix sockets.
// Only the password verifier (auth.Fake), the desktop notifier and the
// identity store (kept in the settings table instead of the keychain) differ
// from production.
package e2e

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/app"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/relayserver"
	"github.com/cravv/cravv-connect/internal/store"
)

// Password is the login password every test machine accepts.
const Password = "correct horse"

// AdminToken is the relay admin token the first machine registers with.
const AdminToken = "e2e-admin-token"

// Relay is an in-process relayserver on a fixed 127.0.0.1 port. Stop drops
// every connection (including hijacked WebSockets) and Start serves again on
// the same address with the same backend, like a relay restart that keeps
// its storage.
type Relay struct {
	t    *testing.T
	addr string
	srv  *relayserver.Server

	mu    sync.Mutex
	hs    *http.Server
	conns map[net.Conn]struct{}
	done  chan struct{}
}

// NewRelay starts a relay on a free port and stops it when the test ends.
func NewRelay(t *testing.T) *Relay { t.Helper(); return NewRelayWith(t, nil) }

// NewRelayWith is NewRelay with the memory backend passed through wrap (when
// not nil), so a test can observe or slow down what the relay stores.
func NewRelayWith(t *testing.T, wrap func(relayserver.Backend) relayserver.Backend) *Relay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &Relay{t: t, addr: ln.Addr().String()}
	var backend relayserver.Backend = relayserver.NewMemoryBackend(core.SystemClock{})
	if wrap != nil {
		backend = wrap(backend)
	}
	srv, err := relayserver.New(relayserver.Config{
		PublicOrigin: r.URL(),
		AdminToken:   AdminToken,
		Clock:        core.SystemClock{},
	}, backend)
	if err != nil {
		t.Fatal(err)
	}
	r.srv = srv
	t.Cleanup(func() { _ = srv.Close() }) // runs after Stop (cleanups are LIFO)
	r.serve(ln)
	t.Cleanup(r.Stop)
	return r
}

// URL is the relay base URL daemons are configured with.
func (r *Relay) URL() string { return "http://" + r.addr }

func (r *Relay) serve(ln net.Listener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns = map[net.Conn]struct{}{}
	r.done = make(chan struct{})
	r.hs = &http.Server{
		Handler: r.srv,
		ConnState: func(c net.Conn, st http.ConnState) {
			r.mu.Lock()
			defer r.mu.Unlock()
			switch st {
			case http.StateNew:
				if r.conns == nil { // stopping: close stragglers at once
					c.Close()
					return
				}
				r.conns[c] = struct{}{}
			case http.StateClosed:
				delete(r.conns, c)
			}
		},
	}
	hs, done := r.hs, r.done
	go func() {
		defer close(done)
		_ = hs.Serve(ln)
	}()
}

// Stop closes the listener and every open connection. It is idempotent.
func (r *Relay) Stop() {
	r.mu.Lock()
	hs, done := r.hs, r.done
	conns := r.conns
	r.hs, r.conns = nil, nil
	r.mu.Unlock()
	if hs == nil {
		return
	}
	_ = hs.Close()
	for c := range conns {
		_ = c.Close() // hijacked WebSocket connections are not closed by hs.Close
	}
	<-done
}

// Start serves again on the same address after Stop.
func (r *Relay) Start() {
	r.t.Helper()
	var ln net.Listener
	var err error
	for range 50 { // the port can take a moment to become free again
		if ln, err = net.Listen("tcp", r.addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		r.t.Fatalf("relay restart on %s: %v", r.addr, err)
	}
	r.serve(ln)
}

// Node is one machine: a daemon, its IPC server, and helpers to open IPC
// connections (each connection is one agent session or one CLI run).
type Node struct {
	t      *testing.T
	Name   string
	Dir    string // CRAVV_HOME-style state directory
	Proj   string // a project folder named "proj" for sessions
	Paths  config.Paths
	Daemon *daemon.Daemon
	Clock  core.Clock

	opts   daemon.Options
	cancel context.CancelFunc
	done   chan struct{}
}

// NodeOptions tune a node. The zero value uses the system clock and no admin token.
type NodeOptions struct {
	// AdminToken registers the mailbox on first connect (cravv-connect init --relay-token).
	AdminToken string
	// Clock replaces the system clock for the daemon and its IPC server.
	Clock core.Clock
}

type nopDesktop struct{}

func (nopDesktop) Notify(string, string) {}

// NewNode builds a daemon configured for relay r, stores the admin token the
// way `cravv-connect init --relay-token` does, and serves the real IPC API on
// a unix socket. Everything stops when the test ends.
func NewNode(t *testing.T, r *Relay, name string, o NodeOptions) *Node {
	t.Helper()
	// Unix socket paths are limited to about 104 bytes, and t.TempDir() paths
	// on macOS are long, so the state directory lives directly under the
	// system temp directory.
	dir, err := os.MkdirTemp("", "cce-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "work", "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	clock := o.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	paths := config.Paths{
		Home: dir, Config: filepath.Join(dir, "config.toml"), DB: filepath.Join(dir, "store.db"),
		Audit: filepath.Join(dir, "audit.log"), Socket: filepath.Join(dir, "d.sock"),
		Files: filepath.Join(dir, "files"), Log: filepath.Join(dir, "daemon.log"),
	}
	cfg := config.Config{RelayURL: r.URL(), DeviceName: name}
	if err := config.Save(paths, cfg); err != nil {
		t.Fatal(err)
	}
	opts := daemon.Options{
		Paths:    paths,
		Config:   cfg,
		Clock:    clock,
		Verifier: auth.Fake{Password: Password},
		Desktop:  nopDesktop{},
		Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore {
			return daemon.SettingsIdentityStore{Settings: s}
		},
		ReconnectMin:     50 * time.Millisecond,
		MaintenanceEvery: time.Hour, // tests call Maintain explicitly
		FileRetryDelay:   100 * time.Millisecond,
	}
	d, err := daemon.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if o.AdminToken != "" {
		if err := d.Settings().SetSetting(context.Background(), daemon.SettingRelayAdminToken, o.AdminToken); err != nil {
			t.Fatal(err)
		}
	}
	n := &Node{t: t, Name: name, Dir: dir, Proj: proj, Paths: paths, Daemon: d, Clock: clock, opts: opts}
	n.start()
	return n
}

// start serves the daemon with app.Serve, the same function
// `cravv-connect daemon run` uses: app.Ports adapts the daemon and
// api.NewServer registers every ipc-v1 method.
func (n *Node) start() {
	n.t.Helper()
	ln, err := ipc.Listen(n.Paths.Socket)
	if err != nil {
		n.t.Fatal(err)
	}
	d := n.Daemon
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel, n.done = cancel, make(chan struct{})
	go func() {
		defer close(n.done)
		if err := app.Serve(ctx, d, ln, n.Clock, slog.New(slog.DiscardHandler)); err != nil && !errors.Is(err, context.Canceled) {
			n.t.Logf("%s: daemon stopped: %v", n.Name, err)
		}
		d.Close()
	}()
	n.t.Cleanup(n.Stop)
}

// Stop stops the daemon and its IPC server. It is idempotent.
func (n *Node) Stop() {
	if n.cancel == nil {
		return
	}
	n.cancel()
	<-n.done
	n.cancel = nil
}

// Restart stops the daemon and starts a new one on the same state directory,
// like a crash or reboot. Open connections end; open new ones afterwards.
func (n *Node) Restart() {
	n.t.Helper()
	n.Stop()
	d, err := daemon.New(n.opts)
	if err != nil {
		n.t.Fatal(err)
	}
	n.Daemon = d
	n.start()
}

// Conn opens a new IPC connection with no session.
func (n *Node) Conn() *ipc.Client {
	n.t.Helper()
	c, err := ipc.Dial(n.Paths.Socket)
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { c.Close() })
	return c
}

// Session opens a connection and registers an agent session in the node's
// project folder. It returns the connection and the assigned session name.
func (n *Node) Session(agent string) (*ipc.Client, string) {
	n.t.Helper()
	c := n.Conn()
	var r ipc.SessionRegisterResult
	if err := c.Call(context.Background(), ipc.MethodSessionRegister,
		ipc.SessionRegisterParams{Agent: agent, ProjectDir: n.Proj, PID: os.Getpid()}, &r); err != nil {
		n.t.Fatalf("%s: session.register: %v", n.Name, err)
	}
	return c, r.Name
}

// Unlocked opens a connection and unlocks it with the password.
func (n *Node) Unlocked() *ipc.Client {
	n.t.Helper()
	c := n.Conn()
	Unlock(n.t, c)
	return c
}

// WaitOnline blocks until the daemon holds a live relay mailbox.
func (n *Node) WaitOnline() {
	n.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := n.Daemon.EnsureRegistered(ctx, ""); err != nil {
		n.t.Fatalf("%s: not online: %v", n.Name, err)
	}
}

// Unlock runs auth.unlock on c with the right password.
func Unlock(t *testing.T, c *ipc.Client) {
	t.Helper()
	if err := c.Call(context.Background(), ipc.MethodAuthUnlock, ipc.UnlockParams{Password: Password}, nil); err != nil {
		t.Fatalf("auth.unlock: %v", err)
	}
}

// Call runs one IPC call with a 30 second limit and fails the test on error.
func Call(t *testing.T, c *ipc.Client, method string, params, result any) {
	t.Helper()
	if err := TryCall(c, method, params, result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// TryCall runs one IPC call with a 30 second limit and returns its error.
func TryCall(c *ipc.Client, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.Call(ctx, method, params, result)
}

// Eventually polls cond every 20ms until it returns true or timeout passes.
func Eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Pair runs the real pairing flow over IPC. a must be online (registered with
// the admin token); b may have no mailbox yet and registers with the invite a
// sends inside the encrypted exchange. Afterwards a knows b by b.Name and b
// knows a by a.Name. Pairing grants no links.
func Pair(t *testing.T, a, b *Node) {
	t.Helper()
	PairBetween(t, a, b, nil)
}

// PairBetween is Pair with between (when not nil) run after b finalized and
// before a does, the window in which b knows a but a does not know b yet.
func PairBetween(t *testing.T, a, b *Node, between func()) {
	t.Helper()
	a.WaitOnline()
	ca, cb := a.Unlocked(), b.Unlocked()

	var start ipc.PairStartResult
	Call(t, ca, ipc.MethodPairStart, nil, &start)

	type awaited struct {
		res ipc.PendingPeerResult
		err error
	}
	awaitc := make(chan awaited, 1)
	go func() {
		var r ipc.PendingPeerResult
		err := TryCall(ca, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: start.PendingID}, &r)
		awaitc <- awaited{r, err}
	}()

	var joined ipc.PendingPeerResult
	Call(t, cb, ipc.MethodJoinStart, ipc.JoinStartParams{Code: start.Code}, &joined)
	if joined.MachineID != string(a.Daemon.Identity().MachineID()) {
		t.Fatalf("join saw machine %s, want %s", joined.MachineID, a.Daemon.Identity().MachineID())
	}
	got := <-awaitc
	if got.err != nil {
		t.Fatalf("pair.await: %v", got.err)
	}
	if got.res.MachineID != string(b.Daemon.Identity().MachineID()) {
		t.Fatalf("await saw machine %s, want %s", got.res.MachineID, b.Daemon.Identity().MachineID())
	}

	var fb ipc.PairFinalizeResult
	Call(t, cb, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: joined.PendingID, Alias: a.Name}, &fb)
	if between != nil {
		between()
	}
	var fa ipc.PairFinalizeResult
	Call(t, ca, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: got.res.PendingID, Alias: b.Name}, &fa)
	if fa.Alias != b.Name || fb.Alias != a.Name {
		t.Fatalf("aliases %q and %q, want %q and %q", fa.Alias, fb.Alias, b.Name, a.Name)
	}
	b.WaitOnline()
}

// NewPair starts a relay and two paired machines, alice (registered with the
// admin token) and bob (registered with alice's invite).
func NewPair(t *testing.T) (*Relay, *Node, *Node) {
	t.Helper()
	return NewPairOn(t, NewRelay(t))
}

// NewPairOn is NewPair on a relay the test started (for example with NewRelayWith).
func NewPairOn(t *testing.T, r *Relay) (*Relay, *Node, *Node) {
	t.Helper()
	a := NewNode(t, r, "alice", NodeOptions{AdminToken: AdminToken})
	b := NewNode(t, r, "bob", NodeOptions{})
	Pair(t, a, b)
	return r, a, b
}

// NewPairWithClock is NewPair with one shared injected clock for both daemons.
func NewPairWithClock(t *testing.T, clock core.Clock) (*Relay, *Node, *Node) {
	t.Helper()
	r := NewRelay(t)
	a := NewNode(t, r, "alice", NodeOptions{AdminToken: AdminToken, Clock: clock})
	b := NewNode(t, r, "bob", NodeOptions{Clock: clock})
	Pair(t, a, b)
	return r, a, b
}

// Inbox drains a session's unread items with inbox.check.
func Inbox(t *testing.T, c *ipc.Client) []ipc.InboxView {
	t.Helper()
	var r ipc.InboxResult
	Call(t, c, ipc.MethodInboxCheck, ipc.InboxCheckParams{Limit: 100}, &r)
	return r.Items
}

// WaitItem collects inbox items with inbox.check until one matches, returning
// it and every item seen (in order).
func WaitItem(t *testing.T, c *ipc.Client, timeout time.Duration, what string, match func(ipc.InboxView) bool) (ipc.InboxView, []ipc.InboxView) {
	t.Helper()
	var seen []ipc.InboxView
	var found *ipc.InboxView
	Eventually(t, timeout, what, func() bool {
		for _, it := range Inbox(t, c) {
			seen = append(seen, it)
			if found == nil && match(it) {
				v := it
				found = &v
			}
		}
		return found != nil
	})
	return *found, seen
}

// Status returns the node's status over IPC.
func (n *Node) Status() ipc.StatusResult {
	n.t.Helper()
	var s ipc.StatusResult
	n.oneCall(ipc.MethodStatus, nil, &s)
	return s
}

// PeerView returns how this node sees the peer with alias, and whether it exists.
func (n *Node) PeerView(alias string) (ipc.PeerView, bool) {
	n.t.Helper()
	var r ipc.PeerListResult
	n.oneCall(ipc.MethodPeerList, nil, &r)
	for _, p := range r.Peers {
		if p.Alias == alias {
			return p, true
		}
	}
	return ipc.PeerView{}, false
}

// oneCall runs a single call on a short-lived connection.
func (n *Node) oneCall(method string, params, result any) {
	n.t.Helper()
	c, err := ipc.Dial(n.Paths.Socket)
	if err != nil {
		n.t.Fatal(err)
	}
	defer c.Close()
	Call(n.t, c, method, params, result)
}
