package auth

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// store.SettingsStore is what the daemon passes to WithState.
var _ StateStore = store.SettingsStore(nil)

// memState is an in-memory StateStore.
type memState struct {
	mu     sync.Mutex
	m      map[string]string
	getErr error
	setErr error
	sets   int
}

func newMemState() *memState { return &memState{m: map[string]string{}} }

func (s *memState) GetSetting(_ context.Context, k string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return "", false, s.getErr
	}
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *memState) SetSetting(_ context.Context, k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.setErr != nil {
		return s.setErr
	}
	s.m[k] = v
	return nil
}

func stateGuard(st StateStore, clk core.Clock) (*Guard, *countingVerifier) {
	cv := &countingVerifier{inner: Fake{Password: "correct horse"}}
	return NewGuard(cv, clk, &memLogger{}, "alice", WithState(st)), cv
}

func TestGuardLockoutSurvivesRestart(t *testing.T) {
	st := newMemState()
	clk := core.NewFakeClock(t0)
	g, _ := stateGuard(st, clk)
	for i := 0; i < core.LockoutFailures; i++ {
		g.Check("wrong")
	}
	if st.m[stateKeyFails] != strconv.Itoa(core.LockoutFailures) {
		t.Fatalf("persisted fails = %q", st.m[stateKeyFails])
	}
	wantUntil := strconv.FormatInt(t0.Add(core.LockoutDuration).UnixMilli(), 10)
	if st.m[stateKeyLockedUntil] != wantUntil {
		t.Fatalf("persisted locked_until = %q, want %q", st.m[stateKeyLockedUntil], wantUntil)
	}

	// "Restart": a new Guard over the same state is still locked.
	clk.Advance(time.Minute)
	g2, cv2 := stateGuard(st, clk)
	if err := g2.Check("correct horse"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("after restart err = %v, want ErrLocked", err)
	}
	if cv2.calls != 0 {
		t.Fatal("locked guard reached the verifier")
	}
	clk.Advance(core.LockoutDuration)
	if err := g2.Check("correct horse"); err != nil {
		t.Fatalf("after expiry err = %v", err)
	}
	if st.m[stateKeyFails] != "0" || st.m[stateKeyLockedUntil] != "0" {
		t.Fatalf("state not reset after success: %v", st.m)
	}
}

func TestGuardFailureCountSurvivesRestart(t *testing.T) {
	st := newMemState()
	clk := core.NewFakeClock(t0)
	g, _ := stateGuard(st, clk)
	for i := 0; i < core.LockoutFailures-1; i++ {
		g.Check("wrong")
	}
	g2, _ := stateGuard(st, clk)
	if err := g2.Check("wrong"); !errors.Is(err, core.ErrBadPassword) {
		t.Fatal(err)
	}
	if err := g2.Check("correct horse"); !errors.Is(err, core.ErrLocked) {
		t.Fatalf("restart reset the failure count: err = %v, want ErrLocked", err)
	}
}

func TestGuardUnreadableStateFailsClosed(t *testing.T) {
	clk := core.NewFakeClock(t0)
	for name, st := range map[string]*memState{
		"get error": {m: map[string]string{}, getErr: errors.New("disk on fire")},
		"bad fails": {m: map[string]string{stateKeyFails: "lots"}},
		"bad until": {m: map[string]string{stateKeyLockedUntil: "soon"}},
	} {
		t.Run(name, func(t *testing.T) {
			g, cv := stateGuard(st, clk)
			if err := g.Check("correct horse"); !errors.Is(err, core.ErrLocked) {
				t.Fatalf("err = %v, want ErrLocked", err)
			}
			if cv.calls != 0 {
				t.Fatal("verifier reached with unreadable state")
			}
		})
	}
}

func TestGuardCapsPersistedLockout(t *testing.T) {
	st := newMemState()
	st.m[stateKeyFails] = "5"
	st.m[stateKeyLockedUntil] = strconv.FormatInt(t0.Add(365*24*time.Hour).UnixMilli(), 10)
	clk := core.NewFakeClock(t0)
	g, _ := stateGuard(st, clk)
	clk.Advance(core.LockoutDuration)
	if err := g.Check("correct horse"); err != nil {
		t.Fatalf("a lockout beyond LockoutDuration was honored: %v", err)
	}
}

func TestGuardPersistErrorDoesNotChangeResult(t *testing.T) {
	st := newMemState()
	st.setErr = errors.New("read-only")
	clk := core.NewFakeClock(t0)
	g, _ := stateGuard(st, clk)
	if err := g.Check("wrong"); !errors.Is(err, core.ErrBadPassword) {
		t.Fatal(err)
	}
	if err := g.Check("correct horse"); err != nil {
		t.Fatal(err)
	}
	if st.sets == 0 {
		t.Fatal("guard never tried to persist")
	}
}
