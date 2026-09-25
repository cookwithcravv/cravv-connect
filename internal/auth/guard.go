package auth

import (
	"errors"
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

	mu          sync.Mutex // held across Verify so attempts are serialized
	fails       int
	lockedUntil time.Time
}

func NewGuard(v Verifier, clock core.Clock, lg audit.Logger, username string) *Guard {
	return &Guard{v: v, clock: clock, lg: lg, username: username}
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
	}

	err := g.v.Verify(g.username, password)
	switch {
	case err == nil:
		g.fails = 0
		g.record(true, nil)
		return nil
	case errors.Is(err, core.ErrBadPassword):
		g.fails++
		detail := map[string]any{"failures": g.fails}
		if g.fails >= core.LockoutFailures {
			g.lockedUntil = now.Add(core.LockoutDuration)
			detail["locked_until"] = g.lockedUntil.UTC()
		}
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
