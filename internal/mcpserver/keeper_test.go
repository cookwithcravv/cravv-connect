package mcpserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// liveConn is a daemon connection that ends when drop is called.
type liveConn struct {
	d    *fakeDaemonLog
	once sync.Once
	done chan struct{}
}

func (c *liveConn) Call(_ context.Context, method string, _, _ any) error {
	select {
	case <-c.done:
		return ipc.ErrClosed
	default:
	}
	c.d.record(method)
	return nil
}
func (c *liveConn) Close() error          { c.drop(); return nil }
func (c *liveConn) Done() <-chan struct{} { return c.done }
func (c *liveConn) drop()                 { c.once.Do(func() { close(c.done) }) }

// fakeDaemonLog is a daemon that may be down; it logs every call.
type fakeDaemonLog struct {
	mu    sync.Mutex
	down  bool
	dials int
	calls []string
	conns []*liveConn
}

func (d *fakeDaemonLog) dial(context.Context) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials++
	if d.down {
		return nil, errors.New("connection refused")
	}
	c := &liveConn{d: d, done: make(chan struct{})}
	d.conns = append(d.conns, c)
	return c, nil
}

func (d *fakeDaemonLog) record(m string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, m)
}

func (d *fakeDaemonLog) setDown(down bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = down
}

// restart drops every connection; the daemon stays down while down is set.
func (d *fakeDaemonLog) dropAll() {
	d.mu.Lock()
	conns := d.conns
	d.mu.Unlock()
	for _, c := range conns {
		c.drop()
	}
}

func (d *fakeDaemonLog) snapshot() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials, count(d.calls, ipc.MethodSessionReattach)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fastKeeper(t *testing.T) {
	t.Helper()
	oldMin, oldMax := reattachBackoffMin, reattachBackoffMax
	reattachBackoffMin, reattachBackoffMax = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { reattachBackoffMin, reattachBackoffMax = oldMin, oldMax })
}

// When the daemon connection drops, a chat that shared a session
// reconnects and reattaches on its own, retrying with backoff while the
// daemon is down, without waiting for a tool call.
func TestSessionReattachesInTheBackground(t *testing.T) {
	fastKeeper(t)
	d := &fakeDaemonLog{}
	s := NewSession(d.dial, "/w/p")
	if _, err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.SetReattach("tok")
	d.setDown(true)
	d.dropAll()
	eventually(t, "several dial attempts while the daemon is down", func() bool { dials, _ := d.snapshot(); return dials >= 3 })
	d.setDown(false)
	eventually(t, "a reattach on a new connection", func() bool { _, re := d.snapshot(); return re == 1 })

	// Idle and connected: nothing more happens.
	time.Sleep(100 * time.Millisecond)
	dials, re := d.snapshot()
	if re != 1 {
		t.Fatalf("%d reattaches while connected", re)
	}

	// After Close the keeper stops: a drop is not followed by a dial.
	s.Close()
	d.dropAll()
	time.Sleep(100 * time.Millisecond)
	if after, _ := d.snapshot(); after != dials {
		t.Fatalf("dialed %d times after Close", after-dials)
	}
}

// A chat that shares nothing does not reconnect in the background, and
// forgetting the token (session_close) stops the keeper.
func TestSessionKeeperOnlyWhileShared(t *testing.T) {
	fastKeeper(t)
	d := &fakeDaemonLog{}
	s := NewSession(d.dial, "/w/p")
	defer s.Close()
	if _, err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.dropAll()
	time.Sleep(100 * time.Millisecond)
	if dials, _ := d.snapshot(); dials != 1 {
		t.Fatalf("an unshared chat dialed %d times", dials)
	}
	if _, err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.SetReattach("tok")
	s.SetReattach("")
	before, _ := d.snapshot()
	d.dropAll()
	time.Sleep(100 * time.Millisecond)
	if dials, _ := d.snapshot(); dials != before {
		t.Fatalf("dialed %d times after the token was forgotten", dials-before)
	}
}
