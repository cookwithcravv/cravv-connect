package ipc

import (
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// ConnState is the per-connection state: the registered session and the
// password unlock window. It is safe for concurrent use because requests on one
// connection are handled concurrently.
type ConnState struct {
	mu            sync.Mutex
	clock         core.Clock
	session       string
	projectDir    string
	unlockedUntil time.Time
}

// NewConnState returns an empty state that reads time from clock.
func NewConnState(clock core.Clock) *ConnState { return &ConnState{clock: clock} }

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
