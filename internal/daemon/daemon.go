package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

// Settings keys owned by the daemon.
const (
	// SettingRelayAdminToken is written once by `cravv-connect init --relay-token`
	// and cleared after the first successful registration.
	SettingRelayAdminToken = "relay_admin_token"
	// SettingRelayInvite holds an invite received while pairing until it is used.
	SettingRelayInvite = "relay_invite"
	// SettingRelayRegistered is "1" once this identity has a relay mailbox.
	SettingRelayRegistered = "relay_registered"
	// SettingAuthSelfTestOK holds GOOS:PAM-service once the password verifier
	// self-test passed for that service; it runs again when the service changes.
	SettingAuthSelfTestOK = "auth_selftest_ok"
)

// RelayFactory builds the relay transports. *relayclient.Client implements it.
type RelayFactory interface {
	Dialer() transport.Dialer
	Rooms() transport.Rooms
	Blobs(s transport.Signer) transport.BlobStore
}

// services is everything built around one identity. ResetIdentity swaps it.
type services struct {
	identity *keys.Identity
	registry *HandlerRegistry
	activity *PeerActivity
	outbound *Outbound
	inbound  *Inbound
	peers    *PeerService
	prekeys  *PrekeyManager
	pairing  *PairingService
	tasks    *TaskService
	files    *FileService
	status   *StatusService
}

// Daemon owns the store, the relay connection and every service.
type Daemon struct {
	opts     Options
	store    store.Store
	audit    audit.Logger
	log      *slog.Logger
	clock    core.Clock
	relay    RelayFactory // nil when no relay is configured
	ids      IdentityStore
	kill     *KillSwitch
	guard    *auth.Guard
	allow    *AllowPaths
	sessions *SessionRegistry
	inbox    *InboxService

	svc        atomic.Pointer[services]
	registered atomic.Bool

	mu        sync.Mutex
	mb        transport.Mailbox
	lastErr   error
	changed   chan struct{} // closed and replaced whenever mb or lastErr changes
	cancelRun context.CancelFunc
	wake      chan struct{} // buffered(1): kicks the connection loop
}

// Mailbox implements MailboxProvider: the live mailbox, or false when offline or killed.
func (d *Daemon) Mailbox() (transport.Mailbox, bool) {
	if d.kill.Killed() {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mb, d.mb != nil
}

// Registered implements Registrar: whether this identity has a relay mailbox.
func (d *Daemon) Registered() bool { return d.registered.Load() }

// EnsureRegistered implements Registrar: it stores the invite, wakes the
// connection loop and waits until a connection (and so a registration)
// succeeds, the relay refuses, or ctx ends.
func (d *Daemon) EnsureRegistered(ctx context.Context, invite string) error {
	if d.relay == nil {
		return errors.New("no relay configured: run cravv-connect init --relay <url>")
	}
	if _, ok := d.Mailbox(); ok {
		return nil
	}
	if invite != "" {
		if err := d.store.SetSetting(ctx, SettingRelayInvite, invite); err != nil {
			return err
		}
	}
	d.mu.Lock()
	d.lastErr = nil
	d.mu.Unlock()
	d.poke()
	for {
		d.mu.Lock()
		mb, lastErr, ch := d.mb, d.lastErr, d.changed
		d.mu.Unlock()
		if mb != nil {
			return nil
		}
		var refused *registerError
		if errors.As(lastErr, &refused) && errors.Is(lastErr, transport.ErrRelayForbidden) {
			return lastErr // a dial that carried credentials was refused
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *Daemon) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// registerError marks a failed dial that carried an admin token or invite.
// EnsureRegistered gives up only on these: a refusal of an earlier dial that
// had nothing to register with (still in flight when the invite was stored)
// must not abort the registration.
type registerError struct{ err error }

func (e *registerError) Error() string { return e.err.Error() }
func (e *registerError) Unwrap() error { return e.err }

// setState records the live mailbox (nil when offline) and the last dial error.
func (d *Daemon) setState(mb transport.Mailbox, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mb, d.lastErr = mb, err
	close(d.changed)
	d.changed = make(chan struct{})
}

// goLive records mb as the live mailbox unless the kill switch is on, in which
// case it closes mb and returns false. The check and the store happen under
// d.mu, which disconnect also takes after the switch flips: either the kill
// sees the live mailbox and closes it, or goLive sees the kill.
func (d *Daemon) goLive(mb transport.Mailbox) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.kill.Killed() {
		mb.Close()
		return false
	}
	d.mb, d.lastErr = mb, nil
	close(d.changed)
	d.changed = make(chan struct{})
	return true
}

// disconnect closes the live mailbox (kill switch).
func (d *Daemon) disconnect() {
	d.mu.Lock()
	mb := d.mb
	d.mu.Unlock()
	if mb != nil {
		mb.Close()
	}
	d.poke()
}

// Run runs the services until ctx ends. After ResetIdentity it continues
// with the rebuilt services.
func (d *Daemon) Run(ctx context.Context) error {
	for {
		g := d.svc.Load()
		gctx, cancel := context.WithCancel(ctx)
		d.mu.Lock()
		d.cancelRun = cancel
		d.mu.Unlock()
		err := d.runServices(gctx, g)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
}

func (d *Daemon) runServices(ctx context.Context, g *services) error {
	if _, err := g.prekeys.EnsureCurrent(ctx); err != nil {
		return fmt.Errorf("prekey: %w", err)
	}
	if err := g.files.Start(ctx); err != nil {
		return fmt.Errorf("resume downloads: %w", err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := g.outbound.Run(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("outbound stopped", "err", err)
		}
	}()
	go func() {
		defer wg.Done()
		d.maintenanceLoop(ctx, g)
	}()
	d.connectLoop(ctx, g)
	wg.Wait()
	g.files.Wait()
	return ctx.Err()
}

// connectLoop keeps one relay connection alive with exponential backoff. When inbound
// stops with ErrRetryLater (a handler failed retryably and the delivery was not acked)
// the connection is recycled so the relay redelivers it; repeated retries back off too.
func (d *Daemon) connectLoop(ctx context.Context, g *services) {
	backoff := d.opts.ReconnectMin
	retryDelay := d.opts.ReconnectMin
	for ctx.Err() == nil {
		if d.kill.Killed() || d.relay == nil {
			d.sleep(ctx, 0)
			continue
		}
		creds, err := d.credentials(ctx)
		if err != nil {
			creds = transport.Credentials{}
			if !d.registered.Load() {
				// An unregistered identity needs its admin token or invite: a dial
				// without them would only be refused. Retry the store later.
				d.log.Error("read relay credentials", "err", err, "retry_in", backoff)
				d.setState(nil, fmt.Errorf("read relay credentials: %w", err))
				d.sleep(ctx, backoff)
				backoff = min(backoff*2, core.BackoffMax)
				continue
			}
			d.log.Warn("read relay credentials; dialing with the existing registration", "err", err)
		}
		mb, err := d.relay.Dialer().Dial(ctx, g.identity, creds)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.log.Warn("relay dial failed", "err", err, "retry_in", backoff)
			if creds.AdminToken != "" || creds.Invite != "" {
				err = &registerError{err: err}
			}
			d.setState(nil, err)
			d.sleep(ctx, backoff)
			backoff = min(backoff*2, core.BackoffMax)
			continue
		}
		d.markRegistered(ctx, creds)
		backoff = d.opts.ReconnectMin
		if d.kill.Killed() {
			mb.Close()
			continue
		}
		if err := g.peers.SyncAllowList(ctx, mb); err != nil {
			d.log.Warn("allow-list sync failed", "err", err)
		}
		if !d.goLive(mb) {
			continue // killed meanwhile
		}
		g.outbound.Wake()
		inErr := g.inbound.Run(ctx, mb)
		if inErr != nil {
			d.log.Warn("inbound stopped", "err", inErr)
		}
		d.setState(nil, mb.Err())
		mb.Close()
		if ctx.Err() != nil {
			return
		}
		if errors.Is(inErr, ErrRetryLater) {
			d.sleep(ctx, retryDelay)
			retryDelay = min(retryDelay*2, core.BackoffMax)
			continue
		}
		retryDelay = d.opts.ReconnectMin
		d.log.Info("relay connection ended", "err", mb.Err())
		d.sleep(ctx, backoff)
	}
}

// sleep waits for dur (0: no timeout), a poke, or ctx.
func (d *Daemon) sleep(ctx context.Context, dur time.Duration) {
	var timeout <-chan time.Time
	if dur > 0 {
		t := time.NewTimer(dur)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-ctx.Done():
	case <-d.wake:
	case <-timeout:
	}
}

func (d *Daemon) credentials(ctx context.Context) (transport.Credentials, error) {
	token, _, err := d.store.GetSetting(ctx, SettingRelayAdminToken)
	if err != nil {
		return transport.Credentials{}, err
	}
	invite, _, err := d.store.GetSetting(ctx, SettingRelayInvite)
	return transport.Credentials{AdminToken: token, Invite: invite}, err
}

// markRegistered records a successful connection and clears one-time credentials.
func (d *Daemon) markRegistered(ctx context.Context, used transport.Credentials) {
	if !d.registered.Load() {
		if err := d.store.SetSetting(ctx, SettingRelayRegistered, "1"); err != nil {
			d.log.Error("persist registration", "err", err)
		}
		d.registered.Store(true)
	}
	if used.AdminToken != "" {
		_ = d.store.SetSetting(ctx, SettingRelayAdminToken, "")
	}
	if used.Invite != "" {
		_ = d.store.SetSetting(ctx, SettingRelayInvite, "")
	}
}

func (d *Daemon) maintenanceLoop(ctx context.Context, g *services) {
	t := time.NewTicker(d.opts.MaintenanceEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.maintain(ctx, g); err != nil {
				d.log.Warn("maintenance", "err", err)
			}
		}
	}
}

// Maintain runs the periodic jobs once (the daemon runs them every minute).
func (d *Daemon) Maintain(ctx context.Context) error { return d.maintain(ctx, d.svc.Load()) }

func (d *Daemon) maintain(ctx context.Context, g *services) error {
	now := d.clock.Now()
	var errs []error
	if !d.kill.Killed() {
		if _, err := g.prekeys.RotateIfDue(ctx); err != nil {
			errs = append(errs, fmt.Errorf("rotate prekey: %w", err))
		}
	}
	if _, err := g.prekeys.Purge(ctx); err != nil {
		errs = append(errs, fmt.Errorf("purge prekeys: %w", err))
	}
	if _, err := g.outbound.PurgeOld(ctx); err != nil {
		errs = append(errs, fmt.Errorf("purge outbox: %w", err))
	}
	if _, err := d.store.PurgeInboxBefore(ctx, now.Add(-core.InboxRetention)); err != nil {
		errs = append(errs, fmt.Errorf("purge inbox: %w", err))
	}
	if _, err := d.store.PurgeFilesBefore(ctx, now.Add(-core.InboxRetention)); err != nil {
		errs = append(errs, fmt.Errorf("purge file records: %w", err))
	}
	if _, err := d.store.PurgeDedupBefore(ctx, now.Add(-core.DedupWindow)); err != nil {
		errs = append(errs, fmt.Errorf("purge dedup: %w", err))
	}
	if _, err := g.tasks.ExpireDue(ctx); err != nil {
		errs = append(errs, fmt.Errorf("expire tasks: %w", err))
	}
	if err := d.sessions.Sweep(ctx); err != nil {
		errs = append(errs, fmt.Errorf("sweep sessions: %w", err))
	}
	return errors.Join(errs...)
}

// ResetIdentity turns the kill switch on (if needed), wipes peers, prekeys and
// the outbox, and switches to a fresh identity. The new identity has no relay
// mailbox yet: the machine registers again with `init --relay-token` or an
// invite received while joining. It is a human-only action: unlocked must be
// true (set by the IPC layer after auth.unlock), otherwise core.ErrAuthRequired.
func (d *Daemon) ResetIdentity(ctx context.Context, unlocked bool) error {
	if !unlocked {
		return core.ErrAuthRequired
	}
	if err := d.kill.Kill(ctx); err != nil {
		return err
	}
	old := d.svc.Load().identity.MachineID()
	peers, err := d.store.ListPeers(ctx)
	if err != nil {
		return err
	}
	for _, p := range peers {
		if err := d.store.DeleteOutboxForPeer(ctx, p.MachineID); err != nil {
			return err
		}
		if err := d.store.DeletePeer(ctx, p.MachineID); err != nil {
			return err
		}
	}
	now := d.clock.Now()
	if err := d.store.SupersedeAllExcept(ctx, "", now); err != nil {
		return err
	}
	if _, err := d.store.DeleteSupersededBefore(ctx, now.Add(time.Second)); err != nil {
		return err
	}
	id, err := keys.GenerateIdentity()
	if err != nil {
		return err
	}
	if err := d.ids.Save(ctx, id.Seed()); err != nil {
		return err
	}
	if err := d.store.SetSetting(ctx, SettingRelayRegistered, ""); err != nil {
		return err
	}
	d.registered.Store(false)
	_ = d.audit.Record(audit.Event{Type: audit.EvResetIdentity, Detail: map[string]any{
		"old_machine_id": string(old), "new_machine_id": string(id.MachineID()),
	}})
	d.svc.Swap(d.build(id)).pairing.Close()
	d.mu.Lock()
	cancel := d.cancelRun
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Close stops background pairing exchanges and releases the store.
func (d *Daemon) Close() error {
	d.svc.Load().pairing.Close()
	return d.store.Close()
}

// Accessors used by the API layer (internal/app adapts them to api ports).

func (d *Daemon) Sessions() *SessionRegistry    { return d.sessions }
func (d *Daemon) Inbox() *InboxService          { return d.inbox }
func (d *Daemon) Tasks() *TaskService           { return d.svc.Load().tasks }
func (d *Daemon) Files() *FileService           { return d.svc.Load().files }
func (d *Daemon) Peers() *PeerService           { return d.svc.Load().peers }
func (d *Daemon) Pairing() *PairingService      { return d.svc.Load().pairing }
func (d *Daemon) Status() *StatusService        { return d.svc.Load().status }
func (d *Daemon) Outbound() *Outbound           { return d.svc.Load().outbound }
func (d *Daemon) Identity() *keys.Identity      { return d.svc.Load().identity }
func (d *Daemon) Kill() *KillSwitch             { return d.kill }
func (d *Daemon) Guard() *auth.Guard            { return d.guard }
func (d *Daemon) AllowPaths() *AllowPaths       { return d.allow }
func (d *Daemon) Audit() audit.Logger           { return d.audit }
func (d *Daemon) AuditPath() string             { return d.opts.Paths.Audit }
func (d *Daemon) Settings() store.SettingsStore { return d.store }
func (d *Daemon) Clock() core.Clock             { return d.clock }
