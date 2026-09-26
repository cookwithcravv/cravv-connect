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

// ProjectDir returns the project folder the session registered with.
func (c *ConnState) ProjectDir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
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
