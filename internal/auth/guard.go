package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
)

// Guard rate-limits password checks: after core.LockoutFailures consecutive
// wrong passwords, every check fails with core.ErrLocked for
// core.LockoutDuration without reaching the Verifier. Every attempt is
// audited as audit.EvPassword with detail {"ok": bool}; the password is
// never logged.
type Guard struct {
	v        Verifier
	clock    core.Clock
	lg       audit.Logger
	username string

	state StateStore // nil = in-memory only

	mu          sync.Mutex // held across Verify so attempts are serialized
	fails       int
	lockedUntil time.Time
}

// StateStore persists the guard's failure count and lockout so a daemon
// restart does not reset them. store.SettingsStore satisfies it.
type StateStore interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Keys under which WithState persists the guard.
const (
	stateKeyFails       = "auth_fails"           // decimal consecutive failures
	stateKeyLockedUntil = "auth_locked_until_ms" // unix ms; 0 = not locked
)

// GuardOption configures NewGuard.
type GuardOption func(*Guard)

// WithState loads the failure count and lockout from s when the guard is
// constructed and writes them back on every change. If the stored state
// cannot be read or parsed the guard fails closed: it starts locked for
// core.LockoutDuration. A stored lockout is never honored for longer than
// core.LockoutDuration from construction.
func WithState(s StateStore) GuardOption { return func(g *Guard) { g.state = s } }

func NewGuard(v Verifier, clock core.Clock, lg audit.Logger, username string, opts ...GuardOption) *Guard {
	g := &Guard{v: v, clock: clock, lg: lg, username: username}
	for _, o := range opts {
		o(g)
	}
	if g.state != nil {
		g.load()
	}
	return g
}

// load reads the persisted state; on any error it locks the guard.
func (g *Guard) load() {
	now := g.clock.Now()
	fails, until, err := g.readState()
	if err != nil {
		g.fails = core.LockoutFailures
		g.lockedUntil = now.Add(core.LockoutDuration)
		g.record(false, map[string]any{"state_error": err.Error(), "locked_until": g.lockedUntil.UTC()})
		return
	}
	if max := now.Add(core.LockoutDuration); until.After(max) {
		until = max
	}
	g.fails, g.lockedUntil = fails, until
}

func (g *Guard) readState() (int, time.Time, error) {
	ctx := context.Background()
	var (
		fails int
		until time.Time
	)
	v, ok, err := g.state.GetSetting(ctx, stateKeyFails)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("auth: read %s: %w", stateKeyFails, err)
	}
	if ok {
		if fails, err = strconv.Atoi(v); err != nil || fails < 0 {
			return 0, time.Time{}, fmt.Errorf("auth: bad %s %q", stateKeyFails, v)
		}
	}
	v, ok, err = g.state.GetSetting(ctx, stateKeyLockedUntil)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("auth: read %s: %w", stateKeyLockedUntil, err)
	}
	if ok {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ms < 0 {
			return 0, time.Time{}, fmt.Errorf("auth: bad %s %q", stateKeyLockedUntil, v)
		}
		if ms > 0 {
			until = time.UnixMilli(ms).UTC()
		}
	}
	return fails, until, nil
}

// persist writes the current state. A write failure is audited but does not
// change the result of the check: the in-memory state still applies.
func (g *Guard) persist() {
	if g.state == nil {
		return
	}
	ctx := context.Background()
	var ms int64
	if !g.lockedUntil.IsZero() {
		ms = g.lockedUntil.UnixMilli()
	}
	err := g.state.SetSetting(ctx, stateKeyFails, strconv.Itoa(g.fails))
	if err == nil {
		err = g.state.SetSetting(ctx, stateKeyLockedUntil, strconv.FormatInt(ms, 10))
	}
	if err != nil {
		_ = g.lg.Record(audit.Event{Type: audit.EvPassword, Detail: map[string]any{
			"user": g.username, "state_error": err.Error(),
		}})
	}
}

// Check verifies password for the guard's user.
// It returns nil, core.ErrLocked, core.ErrBadPassword, or a wrapped
// verifier error (for example ErrUnavailable) that does not count as a failure.
func (g *Guard) Check(password string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock.Now()

	if !g.lockedUntil.IsZero() {
		if now.Before(g.lockedUntil) {
			g.record(false, map[string]any{"locked": true, "locked_until": g.lockedUntil.UTC()})
			return core.ErrLocked
		}
		g.lockedUntil = time.Time{}
		g.fails = 0
		g.persist()
	}

	err := g.v.Verify(g.username, password)
	switch {
	case err == nil:
		if g.fails != 0 {
			g.fails = 0
			g.persist()
		}
		g.record(true, nil)
		return nil
	case errors.Is(err, core.ErrBadPassword):
		g.fails++
		detail := map[string]any{"failures": g.fails}
		if g.fails >= core.LockoutFailures {
			g.lockedUntil = now.Add(core.LockoutDuration)
			detail["locked_until"] = g.lockedUntil.UTC()
		}
		g.persist()
		g.record(false, detail)
		return core.ErrBadPassword
	default:
		g.record(false, map[string]any{"error": err.Error()})
		return err
	}
}

func (g *Guard) record(ok bool, extra map[string]any) {
	detail := map[string]any{"ok": ok, "user": g.username}
	for k, v := range extra {
		detail[k] = v
	}
	// An audit write failure must not turn a correct password into a
	// failure or unlock a locked guard, so it is ignored here.
	_ = g.lg.Record(audit.Event{Type: audit.EvPassword, Detail: detail})
}
