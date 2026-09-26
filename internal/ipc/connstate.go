package ipc

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// ConnState is the per-connection state: the registered session (the
// attachment), the shared session bound to this connection, and the password
// unlock window. It is safe for concurrent use because requests on one
// connection are handled concurrently.
type ConnState struct {
	id            uint64
	mu            sync.Mutex
	clock         core.Clock
	session       string
	agent         string
	projectDir    string
	shared        string
	unlockedUntil time.Time
	run           bool     // the shared session is a managed run's (run token)
	runFolder     string   // that managed session's folder
	fromRun       bool     // the peer process is inside a managed run
	closers       []func() // run when the connection ends
}

// connIDs numbers connections; an ID is never reused within a process.
var connIDs atomic.Uint64

// NewConnState returns an empty state that reads time from clock.
func NewConnState(clock core.Clock) *ConnState {
	return &ConnState{id: connIDs.Add(1), clock: clock}
}

// ID identifies this connection for the lifetime of the process. The daemon
// binds a shared session to it, so identity comes from the connection and
// never from a request argument.
func (c *ConnState) ID() uint64 { return c.id }

// Shared returns the ID of the shared session bound to this connection, or "".
func (c *ConnState) Shared() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shared
}

// SetShared records the shared session bound to this connection ("" unbinds).
func (c *ConnState) SetShared(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shared = id
}

// Session returns the registered session name, or "" if none.
func (c *ConnState) Session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// ProjectDir returns the project folder the session registered with, or
// for a managed run's connection its managed session's folder (the run's
// process chooses its own working folder, so it does not count).
func (c *ConnState) ProjectDir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run {
		return c.runFolder
	}
	return c.projectDir
}

// SetSession records the session registered on this connection.
func (c *ConnState) SetSession(name, projectDir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session, c.projectDir = name, projectDir
}

// Agent returns the agent name the connection registered with.
func (c *ConnState) Agent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agent
}

// SetAgent records the agent name the connection registered with.
func (c *ConnState) SetAgent(agent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agent = agent
}

// Unlock opens the password window for ttl and returns its end.
func (c *ConnState) Unlock(ttl time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unlockedUntil = c.clock.Now().Add(ttl)
	return c.unlockedUntil
}

// Unlocked reports whether the password window is open right now.
func (c *ConnState) Unlocked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clock.Now().Before(c.unlockedUntil)
}

// SetRunBound marks the connection as a managed run's: its shared session
// came from a run token, and only RunMethods may be called on it. folder
// is the managed session's folder.
func (c *ConnState) SetRunBound(folder string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.run, c.runFolder = true, folder
}

// FromRun reports whether the process at the other end is inside a
// managed run (by its peer PID). Until it binds with a run token, such a
// connection may only register and bind.
func (c *ConnState) FromRun() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fromRun
}

func (c *ConnState) setFromRun() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fromRun = true
}

// RunBound reports whether the connection belongs to a managed run.
func (c *ConnState) RunBound() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.run
}

// OnClose registers fn to run once when the connection ends (for example
// to release a hold the connection took).
func (c *ConnState) OnClose(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closers = append(c.closers, fn)
}

// runClosers runs the OnClose functions, newest first.
func (c *ConnState) runClosers() {
	c.mu.Lock()
	fns := c.closers
	c.closers = nil
	c.mu.Unlock()
	for i := len(fns) - 1; i >= 0; i-- {
		fns[i]()
	}
}
