package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// KillFlushTimeout bounds how long Kill waits for the outbox flush that sends
// failed(killed) updates before disconnecting.
const KillFlushTimeout = 3 * time.Second

// KillHooks are the side effects of the kill switch, set by the Daemon.
type KillHooks struct {
	// BeforeKill runs while traffic can still flow: it fails claimed tasks and
	// flushes the outbox (up to KillFlushTimeout) so their senders get
	// task.update failed(killed) now when online (best effort).
	BeforeKill func(ctx context.Context)
	// AfterKill stops running file downloads and disconnects from the relay.
	AfterKill func(ctx context.Context)
	// AfterResume restarts stopped downloads and lets the connection loop dial again.
	AfterResume func(ctx context.Context)
}

// KillSwitch is the persistent "stop everything" switch (spec 10).
type KillSwitch struct {
	settings store.SettingsStore
	audit    audit.Logger

	mu     sync.Mutex // serializes Kill and Resume
	killed atomic.Bool
	hooks  KillHooks
}

// NewKillSwitch loads the persisted state.
func NewKillSwitch(ctx context.Context, settings store.SettingsStore, lg audit.Logger) (*KillSwitch, error) {
	if lg == nil {
		lg = audit.Nop{}
	}
	k := &KillSwitch{settings: settings, audit: lg}
	v, ok, err := settings.GetSetting(ctx, store.SettingKilled)
	if err != nil {
		return nil, err
	}
	k.killed.Store(ok && v == "1")
	return k, nil
}

// SetHooks installs the side effects. Call it before the daemon runs.
func (k *KillSwitch) SetHooks(h KillHooks) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.hooks = h
}

// Killed reports the cached state; it is safe to call on hot paths.
func (k *KillSwitch) Killed() bool { return k.killed.Load() }

// Kill turns the switch on. The state is persisted first so it survives a
// crash in the middle; a second Kill is a no-op.
func (k *KillSwitch) Kill(ctx context.Context) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.killed.Load() {
		return nil
	}
	if err := k.settings.SetSetting(ctx, store.SettingKilled, "1"); err != nil {
		return err
	}
	if k.hooks.BeforeKill != nil {
		k.hooks.BeforeKill(ctx)
	}
	k.killed.Store(true)
	_ = k.audit.Record(audit.Event{Type: audit.EvKill})
	if k.hooks.AfterKill != nil {
		k.hooks.AfterKill(ctx)
	}
	return nil
}

// Resume turns the switch off. It is a human-only action: unlocked must be
// true (set by the IPC layer after auth.unlock), otherwise core.ErrAuthRequired.
func (k *KillSwitch) Resume(ctx context.Context, unlocked bool) error {
	if !unlocked {
		return core.ErrAuthRequired
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.killed.Load() {
		return nil
	}
	if err := k.settings.SetSetting(ctx, store.SettingKilled, ""); err != nil {
		return err
	}
	k.killed.Store(false)
	_ = k.audit.Record(audit.Event{Type: audit.EvKillResume})
	if k.hooks.AfterResume != nil {
		k.hooks.AfterResume(ctx)
	}
	return nil
}
