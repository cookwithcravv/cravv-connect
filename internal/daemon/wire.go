package daemon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/pake"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/store/sqlite"
	"github.com/cravv/cravv-connect/internal/transport"
	"github.com/cravv/cravv-connect/internal/transport/relayclient"
)

// Options configure New. Zero values get production defaults.
type Options struct {
	Paths    config.Paths
	Config   config.Config
	Clock    core.Clock      // default core.SystemClock
	Verifier auth.Verifier   // default auth.NewPAM(Config.PAMService or auth.DefaultPAMService())
	Relay    RelayFactory    // default relayclient.New(Config.RelayURL); nil when no URL
	Desktop  DesktopNotifier // default NewDesktopNotifier()
	Log      *slog.Logger    // default discard

	Username         string                                           // for the password Guard; default auth.CurrentUsername()
	IdentityStore    func(settings store.SettingsStore) IdentityStore // default DefaultIdentityStore
	ReconnectMin     time.Duration                                    // default core.BackoffMin
	MaintenanceEvery time.Duration                                    // default 1 minute
	FileRetryDelay   time.Duration                                    // default 2 seconds
	// StatPAMConfig stats the PAM configuration file for the self-test cache
	// key; default os.Stat.
	StatPAMConfig func(path string) (os.FileInfo, error)
}

// New is the composition root: it opens the store, loads or creates the
// identity, and wires every service and handler.
func New(opts Options) (*Daemon, error) {
	if err := normalize(&opts); err != nil {
		return nil, err
	}
	// A verifier that accepts any password (for example a PAM stack ending
	// in pam_permit) would turn every password gate into a no-op: refuse.
	// The self-test is a failed login against the OS account (pam_faillock
	// counts it), so it runs once per PAM service and configuration file
	// version, and the result is kept in the store. A fresh install runs it
	// before the store is created.
	serviceID := selfTestIdentity(opts.Config, opts.StatPAMConfig)
	_, statErr := os.Stat(opts.Paths.DB)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	tested := false
	if statErr != nil { // no store yet
		if err := selfTest(opts); err != nil {
			return nil, err
		}
		tested = true
	}
	if err := os.MkdirAll(opts.Paths.Files, 0o700); err != nil {
		return nil, err
	}
	db, err := sqlite.Open(opts.Paths.DB)
	if err != nil {
		return nil, err
	}
	if err := recordSelfTest(db, opts, serviceID, tested); err != nil {
		db.Close()
		return nil, err
	}
	d, err := assemble(opts, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

// selfTestIdentity names the password stack the self-test ran against:
// GOOS:service:size:mtime of /etc/pam.d/<service>, or GOOS:service:nostat when
// the file cannot be stat'ed. Editing the PAM file changes the key, so the
// self-test runs again on the next start.
func selfTestIdentity(cfg config.Config, stat func(string) (os.FileInfo, error)) string {
	svc := cfg.PAMService
	if svc == "" {
		svc = auth.DefaultPAMService()
	}
	id := runtime.GOOS + ":" + svc + ":"
	fi, err := stat(filepath.Join("/etc/pam.d", svc))
	if err != nil {
		return id + "nostat"
	}
	return id + strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

func selfTest(opts Options) error {
	if err := auth.SelfTest(opts.Verifier, opts.Username); err != nil {
		return fmt.Errorf("refusing to start: %w", err)
	}
	return nil
}

// recordSelfTest runs the self-test unless it already passed for serviceID
// (or just ran), and stores serviceID once it has passed.
func recordSelfTest(db store.SettingsStore, opts Options, serviceID string, tested bool) error {
	ctx := context.Background()
	v, ok, err := db.GetSetting(ctx, SettingAuthSelfTestOK)
	if err != nil {
		return err
	}
	if ok && v == serviceID {
		return nil
	}
	if !tested {
		if err := selfTest(opts); err != nil {
			return err
		}
	}
	return db.SetSetting(ctx, SettingAuthSelfTestOK, serviceID)
}

func normalize(o *Options) error {
	if o.StatPAMConfig == nil {
		o.StatPAMConfig = os.Stat
	}
	if o.Clock == nil {
		o.Clock = core.SystemClock{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Desktop == nil {
		o.Desktop = NewDesktopNotifier()
	}
	if o.Verifier == nil {
		svc := o.Config.PAMService
		if svc == "" {
			svc = auth.DefaultPAMService()
		}
		o.Verifier = auth.NewPAM(svc)
	}
	if o.Username == "" {
		u, err := auth.CurrentUsername()
		if err != nil {
			return fmt.Errorf("current user: %w", err)
		}
		o.Username = u
	}
	if o.IdentityStore == nil {
		o.IdentityStore = DefaultIdentityStore
	}
	if o.ReconnectMin <= 0 {
		o.ReconnectMin = core.BackoffMin
	}
	if o.MaintenanceEvery <= 0 {
		o.MaintenanceEvery = time.Minute
	}
	if o.FileRetryDelay <= 0 {
		o.FileRetryDelay = 2 * time.Second
	}
	if o.Relay == nil && o.Config.RelayURL != "" {
		c, err := relayclient.New(o.Config.RelayURL)
		if err != nil {
			return err
		}
		o.Relay = c
	}
	return nil
}

func assemble(opts Options, db store.Store) (*Daemon, error) {
	ctx := context.Background()
	lg := audit.NewFileLogger(opts.Paths.Audit, opts.Clock)
	ids := opts.IdentityStore(db)
	identity, err := LoadOrCreateIdentity(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	kill, err := NewKillSwitch(ctx, db, lg)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		opts: opts, store: db, audit: lg, log: opts.Log, clock: opts.Clock, relay: opts.Relay,
		ids: ids, kill: kill, guard: auth.NewGuard(opts.Verifier, opts.Clock, lg, opts.Username, auth.WithState(db)),
		allow: NewAllowPaths(db, lg), changed: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	if v, ok, err := db.GetSetting(ctx, SettingRelayRegistered); err != nil {
		return nil, err
	} else {
		d.registered.Store(ok && v == "1")
	}
	d.sessions = NewSessionRegistry(db, db, opts.Clock)
	if err := d.sessions.DisconnectAll(ctx); err != nil {
		return nil, err
	}
	d.inbox = NewInboxService(db, d.sessions, db, opts.Clock)
	d.sessions.OnExpired(func(ctx context.Context, rec store.SessionRecord) {
		if rec.Agent == CLIAgent {
			return // CLI sessions keep their claimed tasks (a later cli@<dir> continues them)
		}
		if err := d.svc.Load().tasks.AbandonSession(ctx, rec.Name); err != nil {
			d.log.Warn("abandon tasks", "session", rec.Name, "err", err)
		}
	})
	d.sessions.OnExpired(func(ctx context.Context, rec store.SessionRecord) {
		if err := d.inbox.RedirectOrphans(ctx, rec.Name); err != nil {
			d.log.Warn("redirect orphans", "session", rec.Name, "err", err)
		}
	})
	d.svc.Store(d.build(identity))
	kill.SetHooks(KillHooks{
		BeforeKill: func(ctx context.Context) {
			g := d.svc.Load()
			if err := g.tasks.FailActive(ctx, "killed"); err != nil {
				d.log.Warn("fail tasks on kill", "err", err)
			}
			// Send the failed(killed) updates now, while still connected.
			fctx, cancel := context.WithTimeout(ctx, KillFlushTimeout)
			defer cancel()
			if err := g.outbound.SendDue(fctx); err != nil {
				d.log.Warn("flush outbox on kill", "err", err)
			}
		},
		AfterKill: func(context.Context) {
			d.svc.Load().files.StopTransfers()
			d.disconnect()
		},
		AfterResume: func(ctx context.Context) {
			if err := d.svc.Load().files.ResumeDownloads(ctx); err != nil {
				d.log.Warn("resume downloads", "err", err)
			}
			d.poke()
		},
	})
	return d, nil
}

// build wires every identity-bound service and registers all handlers.
func (d *Daemon) build(id *keys.Identity) *services {
	db, clock, lg := d.store, d.clock, d.audit
	g := &services{identity: id, registry: NewHandlerRegistry(), activity: NewPeerActivity(clock)}
	// The send loop keeps sending during the kill flush; everything else stops
	// as soon as Kill starts (Killed).
	g.outbound = NewOutbound(id, db, db, d, clock, func() bool { return !d.kill.SendingAllowed() }, d.log)
	g.peers = NewPeerService(db, d, g.outbound, lg, clock)
	g.prekeys = NewPrekeyManager(db, db, id, g.outbound, clock)
	g.files = NewFileService(FileDeps{
		Blobs: func() transport.BlobStore { return d.blobs(id) }, Peers: db, Files: db, Inbox: d.inbox,
		Sender: g.outbound, Guard: d.allow, FilesDir: d.opts.Paths.Files, Quota: d.opts.Config.PeerQuota,
		Policy: TrustPolicy{}, Clock: clock, Audit: lg, Log: d.log, RetryDelay: d.opts.FileRetryDelay,
		Killed: d.kill.Killed,
	})
	g.tasks = NewTaskService(TaskDeps{
		Tasks: db, Peers: db, Resolver: g.peers, Inbox: d.inbox, Sender: g.outbound, Policy: TrustPolicy{},
		Files: g.files, Desktop: d.opts.Desktop, Clock: clock, Audit: lg,
	})
	g.peers.AddTrustObserver(g.tasks)
	g.peers.AddCutOffObserver(g.tasks)
	g.peers.AddCutOffObserver(g.files)
	g.inbound = NewInbound(id, db, db, g.prekeys, g.registry, g.outbound, clock, d.kill.Killed, d.log)
	g.pairing = NewPairingService(id, d.rooms(), d, pake.SPAKE2{}, db, g.prekeys, d,
		PairingConfig{DeviceName: d.opts.Config.DeviceName, RelayURL: d.opts.Config.RelayURL}, clock, lg)
	registerHandlers(g, d.inbox, db)
	g.status = NewStatusService(StatusDeps{
		MachineID: id.MachineID(), DeviceName: d.opts.Config.DeviceName, RelayURL: d.opts.Config.RelayURL,
		Mailboxes: d, Killed: d.kill.Killed, Peers: db, Outbox: db, Sessions: d.sessions, Inbox: d.inbox,
		Tasks: g.tasks, Activity: g.activity,
		Errors: []func() []string{d.relayErrors, g.outbound.Errors, inboundWarnings(g.inbound)},
	})
	return g
}

// registerHandlers is the single place message kinds are bound to handlers.
func registerHandlers(g *services, inbox *InboxService, peers store.PeerStore) {
	r := g.registry
	r.Register(core.KindChat, NewChatHandler(inbox))
	// task.create and file.offer pass the trust policy centrally (spec 7.1): a rejected
	// item never reaches the service's main handler.
	r.Register(core.KindTaskCreate, PolicyGate{
		Inner: HandlerFunc(g.tasks.HandleCreate), OnReject: HandlerFunc(g.tasks.RejectCreate)})
	r.Register(core.KindTaskUpdate, HandlerFunc(g.tasks.HandleUpdate))
	r.Register(core.KindTaskCancel, HandlerFunc(g.tasks.HandleCancel))
	r.Register(core.KindFileOffer, PolicyGate{
		Inner: HandlerFunc(g.files.HandleOffer), OnReject: HandlerFunc(g.files.RejectOffer)})
	RegisterControlHandlers(r, peers, g.peers, g.outbound)
	g.activity.WrapAll(r,
		core.KindChat, core.KindTaskCreate, core.KindTaskUpdate, core.KindTaskCancel, core.KindFileOffer,
		core.KindControlPrekey, core.KindControlStalePrekey, core.KindControlDelivered, core.KindControlPaused,
		core.KindControlResumed, core.KindControlUnpaired, core.KindControlRelayMoved)
}

func (d *Daemon) relayErrors() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.relay == nil {
		return []string{"no relay configured: run cravv-connect init --relay <url>"}
	}
	if d.mb == nil && d.lastErr != nil && !d.kill.Killed() {
		return []string{"relay offline: " + d.lastErr.Error()}
	}
	return nil
}

func inboundWarnings(in *Inbound) func() []string {
	return func() []string {
		var out []string
		if n := in.Dropped(); n > 0 {
			out = append(out, fmt.Sprintf("%d corrupt or unverifiable frames dropped", n))
		}
		if n := in.SkewRejected(); n > 0 {
			out = append(out, fmt.Sprintf("%d messages rejected for a timestamp in the future: check the clocks", n))
		}
		return out
	}
}

var errNoRelay = errors.New("no relay configured: run cravv-connect init --relay <url>")

func (d *Daemon) rooms() transport.Rooms {
	if d.relay == nil {
		return offlineRooms{}
	}
	return d.relay.Rooms()
}

func (d *Daemon) blobs(id *keys.Identity) transport.BlobStore {
	if d.relay == nil {
		return offlineBlobs{}
	}
	return d.relay.Blobs(id)
}

type offlineRooms struct{}

func (offlineRooms) Open(context.Context, string, string) (transport.Room, error) {
	return nil, errNoRelay
}

type offlineBlobs struct{}

func (offlineBlobs) Create(context.Context, ed25519.PublicKey, int64, uint32) (string, error) {
	return "", errNoRelay
}
func (offlineBlobs) PutChunk(context.Context, string, uint32, []byte) error { return errNoRelay }
func (offlineBlobs) GetChunk(context.Context, string, uint32) ([]byte, error) {
	return nil, errNoRelay
}
func (offlineBlobs) Delete(context.Context, string) error { return errNoRelay }
