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

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/auth"
	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/pake"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/store/sqlite"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// ReconnectStable is how long a relay connection must stay up before the
// reconnect backoff starts over from Options.ReconnectMin.
const ReconnectStable = 60 * time.Second

// Options configure New. Zero values get production defaults.
type Options struct {
	Paths    config.Paths
	Config   config.Config
	Version  string          // the binary's version, reported by status
	Clock    core.Clock      // default core.SystemClock
	Verifier auth.Verifier   // default auth.NewPAM(Config.PAMService or auth.DefaultPAMService())
	Relay    RelayFactory    // default relayclient.New(Config.RelayURL); nil when no URL
	Desktop  DesktopNotifier // default NewDesktopNotifier()
	Log      *slog.Logger    // default discard

	Username         string                                           // for the password Guard; default auth.CurrentUsername()
	IdentityStore    func(settings store.SettingsStore) IdentityStore // default DefaultIdentityStore
	ReconnectMin     time.Duration                                    // default core.BackoffMin
	ReconnectStable  time.Duration                                    // a connection this long resets the backoff; default 60 s
	MaintenanceEvery time.Duration                                    // default 1 minute
	PresenceEvery    time.Duration                                    // default core.PresenceInterval
	FileRetryDelay   time.Duration                                    // default 2 seconds
	// StatPAMConfig stats the PAM configuration file for the self-test cache
	// key; default os.Stat.
	StatPAMConfig func(path string) (os.FileInfo, error)
	// ClaudeCheck says whether managed runs could start claude, checked
	// when an offer is set; default: ResolveClaude finds an executable.
	ClaudeCheck func() error
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
	var tested *selfTestResult
	if statErr != nil { // no store yet
		r, err := selfTest(opts)
		if err != nil {
			return nil, err
		}
		tested = &r
	}
	if err := os.MkdirAll(opts.Paths.Files, 0o700); err != nil {
		return nil, err
	}
	db, err := sqlite.Open(opts.Paths.DB)
	if err != nil {
		return nil, err
	}
	authWarning, err := recordSelfTest(db, opts, serviceID, tested)
	if err != nil {
		db.Close()
		return nil, err
	}
	d, err := assemble(opts, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	if authWarning != "" {
		d.authWarning = authWarning
		d.log.Warn("password check is not working; password-gated actions will fail", "err", authWarning)
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

// selfTestResult is a self-test that did not refuse the start: passed, or
// found that passwords cannot be checked at all (warning set).
type selfTestResult struct{ warning string }

// selfTest runs the verifier self-test. A verifier that accepts a random
// password refuses the start. One that fails for another reason (no PAM, a
// PAM service error) cannot check passwords: every unlock fails, which is
// safe, so the daemon starts and reports it in status instead.
func selfTest(opts Options) (selfTestResult, error) {
	err := auth.SelfTest(opts.Verifier, opts.Username)
	switch {
	case err == nil:
		return selfTestResult{}, nil
	case errors.Is(err, auth.ErrCheckNotWorking):
		return selfTestResult{warning: err.Error()}, nil
	default:
		return selfTestResult{}, fmt.Errorf("refusing to start: %w", err)
	}
}

// recordSelfTest runs the self-test unless it already passed for serviceID
// (or just ran), and stores serviceID once it has passed. It returns the
// status warning when passwords cannot be checked; that result is not
// stored, so the test runs again on the next start.
func recordSelfTest(db store.SettingsStore, opts Options, serviceID string, tested *selfTestResult) (string, error) {
	ctx := context.Background()
	v, ok, err := db.GetSetting(ctx, SettingAuthSelfTestOK)
	if err != nil {
		return "", err
	}
	if ok && v == serviceID {
		return "", nil
	}
	if tested == nil {
		r, err := selfTest(opts)
		if err != nil {
			return "", err
		}
		tested = &r
	}
	if tested.warning != "" {
		return tested.warning, nil
	}
	return "", db.SetSetting(ctx, SettingAuthSelfTestOK, serviceID)
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
	if o.ReconnectStable <= 0 {
		o.ReconnectStable = ReconnectStable
	}
	if o.MaintenanceEvery <= 0 {
		o.MaintenanceEvery = time.Minute
	}
	if o.PresenceEvery <= 0 {
		o.PresenceEvery = core.PresenceInterval
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
	d.sessions = NewSessionRegistry(db, opts.Clock)
	if err := d.sessions.DisconnectAll(ctx); err != nil {
		return nil, err
	}
	d.shared = NewSessionService(db, opts.Clock)
	d.shared.AddObserver(sessionLinks{d})
	d.inbox = NewInboxService(db, d.shared, db, db, opts.Clock)
	d.inbox.AddReadObserver(taskReader{d})
	d.attend = NewAttentionService(AttentionDeps{Sessions: d.shared, Inbox: db, Links: db, Tasks: db, Peers: db, Changes: d.inbox})
	d.shared.AddObserver(d.attend)
	d.codes = NewConfirmCodes(opts.Clock, opts.Desktop, WithCodeState(db))
	d.hooks = NewHookService(d.shared, d.attend)
	d.assembleManaged(db, lg)
	d.svc.Store(d.build(identity))
	// No connection survives a restart: every open session is away until
	// its client reattaches (links stay open for the away grace).
	if err := d.shared.AwayAll(ctx); err != nil {
		return nil, err
	}
	kill.SetHooks(KillHooks{
		BeforeKill: func(ctx context.Context) {
			d.host.StopAll() // managed runs end first: their process groups are killed
			g := d.svc.Load()
			if err := g.tasks.FailActive(ctx, "killed"); err != nil {
				d.log.Warn("fail tasks on kill", "err", err)
			}
			if err := g.links.CloseAll(ctx, core.CloseKilled); err != nil {
				d.log.Warn("close links on kill", "err", err)
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
	g.outbound.SetPauseRecorder(g.peers)
	g.discover = NewDiscovery(d.shared, g.peers, g.outbound, clock, d.log)
	g.replies = NewLinkReplies(g.outbound, clock, d.log)
	g.links = NewLinkService(LinkDeps{
		Links: db, Sessions: d.shared, Peers: db, Directory: g.discover, Sender: g.outbound, Replies: g.replies,
		Inbox: d.inbox, Desktop: d.opts.Desktop, Managed: d.host, Unsent: g.outbound, Clock: clock, Audit: lg, Log: d.log,
	})
	g.presence = NewPresenceService(db, db, g.links, g.outbound, clock, d.log)
	g.presence.SetGrace(presenceGrace(d.shared, db, offersNow{d}))
	g.presence.SetOnline(func() bool { _, ok := d.Mailbox(); return ok })
	g.versions = NewVersionNotices()
	g.prekeys = NewPrekeyManager(db, db, id, g.outbound, clock)
	g.files = NewFileService(FileDeps{
		Blobs: func() transport.BlobStore { return d.blobs(id) }, Peers: db, Links: db, Sessions: d.shared, Files: db, Inbox: d.inbox,
		Sender: g.outbound, Guard: d.allow, FilesDir: d.opts.Paths.Files, Quota: d.opts.Config.PeerQuota,
		Policy: PermissionPolicy{}, Clock: clock, Audit: lg, Log: d.log, RetryDelay: d.opts.FileRetryDelay,
		Killed: d.kill.Killed,
	})
	g.tasks = NewTaskService(TaskDeps{
		Tasks: db, Peers: db, Links: g.links, Lookup: db, Inbox: d.inbox, Sender: g.outbound,
		Policy: PermissionPolicy{}, Files: g.files, Desktop: d.opts.Desktop, Clock: clock, Audit: lg,
	})
	g.review = NewReviewService(ReviewDeps{
		Links: db, Tasks: db, Peers: db, LinkSvc: g.links, TaskSvc: g.tasks, Codes: d.codes, Clock: clock,
	})
	g.peers.AddCutOffObserver(g.tasks)
	g.peers.AddCutOffObserver(g.files)
	g.peers.AddCutOffObserver(g.links)
	// A closed link fails its tasks, declines its held files and drops what
	// an away session had not read yet.
	g.links.AddCloseObserver(g.tasks)
	g.links.AddCloseObserver(g.files)
	g.links.AddCloseObserver(d.inbox)
	g.links.AddLowerObserver(g.tasks)
	d.buildManaged(g)
	g.inbound = NewInbound(id, db, db, g.prekeys, g.registry, g.outbound, clock, d.kill.Killed, d.log)
	g.limiter = NewInboundLimiter(d.inbox, DefaultInboundLimits(), clock, lg)
	g.pairing = NewPairingService(id, d.rooms(), d, pake.SPAKE2{}, db, g.prekeys, g.outbound, d,
		PairingConfig{DeviceName: d.opts.Config.DeviceName, RelayURL: d.opts.Config.RelayURL}, clock, lg)
	registerHandlers(g, d.inbox, d.shared, db)
	g.status = NewStatusService(StatusDeps{
		Version: d.opts.Version, MachineID: id.MachineID(), DeviceName: d.opts.Config.DeviceName, RelayURL: d.opts.Config.RelayURL,
		Mailboxes: d, Killed: d.kill.Killed, Peers: db, Outbox: db, Shared: d.shared, Inbox: d.inbox,
		Tasks: g.tasks, Activity: g.activity,
		Errors: []func() []string{d.authErrors, d.relayErrors, g.outbound.Errors, inboundWarnings(g.inbound),
			g.limiter.Warnings, g.versions.Errors},
	})
	return g
}

// registerHandlers is the single place message kinds are bound to handlers.
func registerHandlers(g *services, inbox *InboxService, sessions SessionLookup, db store.Store) {
	r := g.registry
	// Every link-scoped kind passes the LinkGate (v2 spec 10): an envelope
	// without an active link, from the wrong machine, or not permitted on
	// the link never reaches the service's main handler.
	gate := func(inner, onReject Handler) Handler {
		return LinkGate{Links: db, Sessions: sessions, Replies: g.versions.Replier(g.replies), Inner: inner, OnReject: onReject, Seen: g.versions.Seen,
			Traffic: g.presence.Traffic}
	}
	// Chat and task.update are what a link can pile up here: both pass the
	// per-link inbound limits.
	r.Register(core.KindChat, gate(g.limiter.Wrap(NewChatHandler(inbox)), nil))
	r.Register(core.KindTaskCreate, gate(HandlerFunc(g.tasks.HandleCreate), HandlerFunc(g.tasks.RejectCreate)))
	r.Register(core.KindTaskUpdate, gate(g.limiter.Wrap(HandlerFunc(g.tasks.HandleUpdate)), nil))
	r.Register(core.KindTaskCancel, gate(HandlerFunc(g.tasks.HandleCancel), nil))
	r.Register(core.KindFileOffer, gate(HandlerFunc(g.files.HandleOffer), HandlerFunc(g.files.RejectOffer)))
	RegisterControlHandlers(r, db, g.peers, g.outbound)
	r.Register(core.KindControlUnsupported, HandlerFunc(g.versions.HandleUnsupported))
	r.Register(core.KindSessionsList, HandlerFunc(g.discover.HandleList))
	r.Register(core.KindSessionsListed, HandlerFunc(g.discover.HandleListed))
	r.Register(core.KindLinkRequest, HandlerFunc(g.links.HandleRequest))
	r.Register(core.KindLinkAccepted, HandlerFunc(g.links.HandleAccepted))
	r.Register(core.KindLinkRejected, HandlerFunc(g.links.HandleRejected))
	r.Register(core.KindLinkClosed, HandlerFunc(g.links.HandleClosed))
	r.Register(core.KindLinkState, HandlerFunc(g.links.HandleState))
	r.Register(core.KindPresencePing, HandlerFunc(g.presence.HandlePing))
	r.Register(core.KindPresencePong, HandlerFunc(g.presence.HandlePong))
	g.activity.WrapAll(r,
		core.KindChat, core.KindTaskCreate, core.KindTaskUpdate, core.KindTaskCancel, core.KindFileOffer,
		core.KindControlPrekey, core.KindControlStalePrekey, core.KindControlDelivered, core.KindControlPaused,
		core.KindControlResumed, core.KindControlUnpaired, core.KindControlRelayMoved, core.KindControlUnsupported,
		core.KindSessionsList, core.KindSessionsListed, core.KindLinkRequest, core.KindLinkAccepted,
		core.KindLinkRejected, core.KindLinkClosed, core.KindLinkState, core.KindPresencePing, core.KindPresencePong)
}

// taskReader forwards inbox reads to the current TaskService (ResetIdentity
// replaces the services; the inbox and its observer stay).
type taskReader struct{ d *Daemon }

func (o taskReader) ItemsRead(ctx context.Context, session string, items []store.InboxItem) {
	o.d.svc.Load().tasks.ItemsRead(ctx, session, items)
}

// sessionLinks forwards shared-session changes to the current LinkService
// (ResetIdentity replaces the services; the observer stays registered).
type sessionLinks struct{ d *Daemon }

func (o sessionLinks) SessionAway(ctx context.Context, s store.SharedSession) {
	o.d.svc.Load().links.SessionAway(ctx, s)
}
func (o sessionLinks) SessionBack(ctx context.Context, s store.SharedSession) {
	o.d.svc.Load().links.SessionBack(ctx, s)
}
func (o sessionLinks) SessionClosed(ctx context.Context, s store.SharedSession) {
	o.d.svc.Load().links.SessionClosed(ctx, s)
}

// authErrors reports a verifier that cannot check passwords (set by New).
func (d *Daemon) authErrors() []string {
	if d.authWarning == "" {
		return nil
	}
	return []string{d.authWarning}
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
		drops := in.Drops()
		if drops.Corrupt > 0 {
			out = append(out, fmt.Sprintf("%d corrupt or unverifiable frames dropped", drops.Corrupt))
		}
		if drops.Unknown > 0 {
			out = append(out, fmt.Sprintf("%d messages from unknown machines dropped", drops.Unknown))
		}
		if drops.Paused > 0 {
			out = append(out, fmt.Sprintf("%d messages from machines you paused dropped", drops.Paused))
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
