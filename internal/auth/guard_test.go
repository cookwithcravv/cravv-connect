package auth

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// countingVerifier wraps a Verifier and counts how often it is reached.
type countingVerifier struct {
	mu    sync.Mutex
	inner Verifier
	calls int
	users []string
}

func (c *countingVerifier) Verify(u, p string) error {
	c.mu.Lock()
	c.calls++
	c.users = append(c.users, u)
	c.mu.Unlock()
	return c.inner.Verify(u, p)
}

// memLogger captures audit events.
type memLogger struct {
	mu  sync.Mutex
	evs []audit.Event
}

func (m *memLogger) Record(e audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evs = append(m.evs, e)
	return nil
}

func newGuard() (*Guard, *countingVerifier, *memLogger, *core.FakeClock) {
	cv := &countingVerifier{inner: Fake{Password: "correct horse"}}
	lg := &memLogger{}
	clk := core.NewFakeClock(t0)
	return NewGuard(cv, clk, lg, "alice"), cv, lg, clk
}

func TestGuardLocksAfterFiveFailures(t *testing.T) {
	g, cv, _, clk := newGuard()
	for i := 1; i <= core.LockoutFailures; i++ {
		if err := g.Check("wrong"); !errors.Is(err, core.ErrBadPassword) {
			t.Fatalf("attempt %d: err = %v, want ErrBadPassword", i, err)
		}
	}
	// Locked now: even the correct password is refused and the verifier is not reached.
	if err := g.Check("correct horse"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("after 5 failures err = %v, want ErrLocked", err)
	}
	clk.Advance(core.LockoutDuration - time.Second)
	if err := g.Check("correct horse"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("just before unlock err = %v, want ErrLocked", err)
	}
	if cv.calls != core.LockoutFailures {
		t.Fatalf("verifier called %d times, want %d (locked attempts must not reach it)", cv.calls, core.LockoutFailures)
	}
	clk.Advance(time.Second)
	if err := g.Check("correct horse"); err != nil {
		t.Fatalf("after 15 minutes err = %v, want nil", err)
	}
	if cv.users[0] != "alice" {
		t.Fatalf("verified user %q, want alice", cv.users[0])
	}
}

func TestGuardLockoutRestartsCountAfterExpiry(t *testing.T) {
	g, _, _, clk := newGuard()
	for i := 0; i < core.LockoutFailures; i++ {
		g.Check("wrong")
	}
	clk.Advance(core.LockoutDuration)
	// A fresh window: four more failures do not lock.
	for i := 0; i < core.LockoutFailures-1; i++ {
		if err := g.Check("wrong"); !errors.Is(err, core.ErrBadPassword) {
			t.Fatalf("err = %v", err)
		}
	}
	if err := g.Check("correct horse"); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestGuardSuccessResetsCounter(t *testing.T) {
	g, _, _, _ := newGuard()
	for i := 0; i < core.LockoutFailures-1; i++ {
		g.Check("wrong")
	}
	if err := g.Check("correct horse"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < core.LockoutFailures-1; i++ {
		if err := g.Check("wrong"); !errors.Is(err, core.ErrBadPassword) {
			t.Fatalf("failure %d after reset: err = %v", i+1, err)
		}
	}
	if err := g.Check("correct horse"); err != nil {
		t.Fatalf("locked too early after a reset: %v", err)
	}
}

func TestGuardAuditsEveryAttemptWithoutPassword(t *testing.T) {
	g, _, lg, _ := newGuard()
	const secret = "s3cr3t-Pa55word"
	g.Check("correct horse")
	for i := 0; i < core.LockoutFailures; i++ {
		g.Check(secret)
	}
	g.Check(secret) // locked
	if len(lg.evs) != 1+core.LockoutFailures+1 {
		t.Fatalf("audited %d attempts, want %d", len(lg.evs), 1+core.LockoutFailures+1)
	}
	wantOK := []bool{true, false, false, false, false, false, false}
	for i, e := range lg.evs {
		if e.Type != audit.EvPassword {
			t.Fatalf("event %d type %q", i, e.Type)
		}
		if ok, _ := e.Detail["ok"].(bool); ok != wantOK[i] {
			t.Fatalf("event %d ok = %v, want %v", i, e.Detail["ok"], wantOK[i])
		}
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "correct horse") {
			t.Fatalf("password leaked into audit event: %s", raw)
		}
	}
	if locked, _ := lg.evs[len(lg.evs)-1].Detail["locked"].(bool); !locked {
		t.Fatalf("locked attempt not marked: %v", lg.evs[len(lg.evs)-1].Detail)
	}
}

func TestGuardVerifierErrorIsNotAFailure(t *testing.T) {
	lg := &memLogger{}
	g := NewGuard(unavailable{}, core.NewFakeClock(t0), lg, "alice")
	for i := 0; i < core.LockoutFailures+2; i++ {
		if err := g.Check("pw"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable (never ErrLocked)", err)
		}
	}
	if len(lg.evs) != core.LockoutFailures+2 {
		t.Fatalf("audited %d, want %d", len(lg.evs), core.LockoutFailures+2)
	}
}

func TestGuardConcurrentChecksCountEveryFailure(t *testing.T) {
	g, cv, _, _ := newGuard()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Check("wrong")
		}()
	}
	wg.Wait()
	if cv.calls != core.LockoutFailures {
		t.Fatalf("verifier reached %d times by 20 concurrent wrong attempts, want %d", cv.calls, core.LockoutFailures)
	}
}
