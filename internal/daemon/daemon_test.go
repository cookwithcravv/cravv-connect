package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

// d2Mailbox is a scripted relay connection. Close ends it like a dropped socket.
type d2Mailbox struct {
	mu         sync.Mutex
	allowed    []ed25519.PublicKey
	sends      int
	deliveries chan transport.Delivery
	done       chan struct{}
	once       sync.Once
}

func newD2Mailbox() *d2Mailbox {
	return &d2Mailbox{deliveries: make(chan transport.Delivery), done: make(chan struct{})}
}

func (m *d2Mailbox) Send(context.Context, core.MachineID, string, []byte) (transport.SendStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sends++
	return transport.SendQueued, nil
}
func (m *d2Mailbox) Deliveries() <-chan transport.Delivery { return m.deliveries }
func (m *d2Mailbox) Ack(context.Context, uint64) error     { return nil }
func (m *d2Mailbox) Allow(_ context.Context, ik ed25519.PublicKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowed = append(m.allowed, ik)
	return nil
}
func (m *d2Mailbox) Deny(context.Context, ed25519.PublicKey) error      { return nil }
func (m *d2Mailbox) RequestInvite(context.Context) (string, error)      { return "INVITE", nil }
func (m *d2Mailbox) CreateRoom(context.Context) (string, string, error) { return "ABCD", "tok", nil }
func (m *d2Mailbox) Done() <-chan struct{}                              { return m.done }
func (m *d2Mailbox) Err() error                                         { return errors.New("connection closed") }
func (m *d2Mailbox) Close() error {
	m.once.Do(func() {
		close(m.done)
		close(m.deliveries)
	})
	return nil
}

func (m *d2Mailbox) closed() bool {
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}

func (m *d2Mailbox) sendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sends
}

func (m *d2Mailbox) isAllowed(ik ed25519.PublicKey) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.allowed {
		if bytes.Equal(a, ik) {
			return true
		}
	}
	return false
}

// d2Relay is a RelayFactory whose Dialer hands out a fresh d2Mailbox per dial.
type d2Relay struct {
	mu     sync.Mutex
	creds  []transport.Credentials
	boxes  []*d2Mailbox
	refuse func(transport.Credentials) error
}

func (r *d2Relay) Dialer() transport.Dialer                   { return r }
func (r *d2Relay) Rooms() transport.Rooms                     { return offlineRooms{} }
func (r *d2Relay) Blobs(transport.Signer) transport.BlobStore { return offlineBlobs{} }

func (r *d2Relay) Dial(_ context.Context, _ transport.Signer, c transport.Credentials) (transport.Mailbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creds = append(r.creds, c)
	if r.refuse != nil {
		if err := r.refuse(c); err != nil {
			return nil, err
		}
	}
	mb := newD2Mailbox()
	r.boxes = append(r.boxes, mb)
	return mb, nil
}

func (r *d2Relay) dials() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.creds)
}

func (r *d2Relay) box(i int) *d2Mailbox {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.boxes[i]
}

func (r *d2Relay) cred(i int) transport.Credentials {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creds[i]
}

func d2Options(dir string, relay RelayFactory) Options {
	return Options{
		Paths: config.Paths{
			Home: dir, Config: filepath.Join(dir, "config.toml"), DB: filepath.Join(dir, "store.db"),
			Audit: filepath.Join(dir, "audit.log"), Socket: filepath.Join(dir, "daemon.sock"),
			Files: filepath.Join(dir, "files"), Log: filepath.Join(dir, "daemon.log"),
		},
		Config:           config.Config{RelayURL: "https://relay.test", DeviceName: "test-mac"},
		Verifier:         auth.Fake{Password: "pw"},
		Relay:            relay,
		Desktop:          &d2Desktop{},
		Username:         "tester",
		IdentityStore:    func(s store.SettingsStore) IdentityStore { return SettingsIdentityStore{Settings: s} },
		ReconnectMin:     10 * time.Millisecond,
		MaintenanceEvery: time.Hour,
		FileRetryDelay:   time.Millisecond,
	}
}

func d2NewDaemon(t *testing.T, dir string, relay RelayFactory) *Daemon {
	t.Helper()
	d, err := New(d2Options(dir, relay))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// d2Run runs d until the returned stop func is called (also on cleanup).
func d2Run(t *testing.T, d *Daemon) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-errc:
				if err != nil {
					t.Errorf("Run: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Run did not stop")
			}
			d.Close()
		})
	}
	t.Cleanup(stop)
	return stop
}

func d2Eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewWiresHandlersAndResetsSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d := d2NewDaemon(t, dir, &d2Relay{})
	name, err := d.Sessions().Register(ctx, "claude", "/w/proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []core.Kind{core.KindChat, core.KindTaskCreate, core.KindTaskUpdate, core.KindTaskCancel,
		core.KindFileOffer, core.KindControlPrekey, core.KindControlStalePrekey, core.KindControlDelivered,
		core.KindControlPaused, core.KindControlResumed, core.KindControlUnpaired, core.KindControlRelayMoved,
		core.KindControlUnsupported, core.KindSessionsList, core.KindSessionsListed, core.KindLinkRequest,
		core.KindLinkAccepted, core.KindLinkRejected, core.KindLinkClosed, core.KindLinkState,
		core.KindPresencePing, core.KindPresencePong} {
		if _, ok := d.svc.Load().registry.Lookup(k); !ok {
			t.Errorf("no handler for %s", k)
		}
	}
	shared := d2Share(t, d.Shared(), "lead")
	id := d.Identity().MachineID()
	d.Close()

	d2 := d2NewDaemon(t, dir, &d2Relay{})
	defer d2.Close()
	if d2.Identity().MachineID() != id {
		t.Fatal("identity changed across restarts")
	}
	if d2.Sessions().Connected(ctx, name) {
		t.Fatal("session still marked connected after a daemon restart")
	}
	if again, _ := d2.Sessions().Register(ctx, "claude", "/w/proj"); again != name {
		t.Fatalf("reclaim after restart got %q, want %q", again, name)
	}
	// No connection survives a restart: the shared session is away, not closed.
	if s, err := d2.Shared().Get(ctx, shared.ID); err != nil || s.State != core.SessionAway {
		t.Fatalf("shared session after restart = %+v, %v", s, err)
	}
}

func TestLifecycleSyncAndReconnect(t *testing.T) {
	ctx := context.Background()
	relay := &d2Relay{}
	d := d2NewDaemon(t, t.TempDir(), relay)
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	if err := d.Settings().SetSetting(ctx, SettingRelayAdminToken, "admin-secret"); err != nil {
		t.Fatal(err)
	}
	d2Run(t, d)

	d2Eventually(t, "first connection", func() bool { _, ok := d.Mailbox(); return ok })
	if c := relay.cred(0); c.AdminToken != "admin-secret" {
		t.Fatalf("first dial creds = %+v", c)
	}
	if !relay.box(0).isAllowed(peer.rec.IK) {
		t.Fatal("peer not on the allow-list when the mailbox went live")
	}
	if v, _, _ := d.Settings().GetSetting(ctx, SettingRelayAdminToken); v != "" || !d.Registered() {
		t.Fatalf("admin token %q registered %v", v, d.Registered())
	}
	if st, _ := d.Status().Status(ctx); !st.RelayConnected {
		t.Fatal("status not connected")
	}

	relay.box(0).Close() // the relay drops the socket
	d2Eventually(t, "reconnect", func() bool { return relay.dials() >= 2 && relay.box(1) != nil })
	d2Eventually(t, "second allow-list sync", func() bool { return relay.box(1).isAllowed(peer.rec.IK) })
	if c := relay.cred(1); c.AdminToken != "" || c.Invite != "" {
		t.Fatalf("reconnect reused one-time credentials: %+v", c)
	}
}

func TestKillBlocksOutboundAndPersists(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	relay := &d2Relay{}
	d := d2NewDaemon(t, dir, relay)
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	stop := d2Run(t, d)
	d2Eventually(t, "connection", func() bool { _, ok := d.Mailbox(); return ok })

	// A claimed inbound task is failed with "killed" when the switch flips.
	session := d2Share(t, d.Shared(), "lead")
	link := d2Link(t, d.store, peer.rec, session, "trainer", core.PermTasksAuto, core.PermMessages)
	taskID := core.NewID()
	env := d2Env(t, peer.rec, core.KindTaskCreate, link.ID, core.TaskCreateBody{TaskID: taskID, Instructions: "work"})
	if err := d.Tasks().HandleCreate(withLink(ctx, link), peer.rec, env); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Tasks().Claim(ctx, session.ID, taskID); err != nil {
		t.Fatal(err)
	}
	d2Eventually(t, "claimed update sent", func() bool { return relay.box(0).sendCount() >= 1 })
	sent := relay.box(0).sendCount()

	if err := d.Kill().Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Mailbox(); ok {
		t.Fatal("mailbox still available after kill")
	}
	d2Eventually(t, "relay disconnect", relay.box(0).closed)
	if tk, _ := d.store.GetTask(ctx, taskID); tk.State != core.TaskFailed || tk.Notes[len(tk.Notes)-1].Text != "killed" {
		t.Fatalf("claimed task after kill = %+v", tk)
	}
	if l, _ := d.store.GetLink(ctx, peer.rec.MachineID, link.ID); l.State != store.LinkClosed || l.Reason != core.CloseKilled {
		t.Fatalf("link after kill = %+v", l)
	}
	// The failed(killed) update and link.closed(killed) went out before the disconnect.
	if got := relay.box(0).sendCount(); got != sent+2 {
		t.Fatalf("sends at kill %d->%d, want the failed(killed) update and link.closed flushed", sent, got)
	}
	sent += 2
	// Envelopes are still queued while killed (the IPC layer refuses agent sends);
	// nothing leaves until resume.
	if _, err := d.Outbound().SendEnvelope(ctx, peer.rec.MachineID, core.KindChat, "", core.ChatBody{Text: "x"}); err != nil {
		t.Fatalf("enqueue while killed err = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if relay.dials() != 1 || relay.box(0).sendCount() != sent {
		t.Fatalf("traffic while killed: dials %d sends %d->%d", relay.dials(), sent, relay.box(0).sendCount())
	}
	st, err := d.Status().Status(ctx)
	if err != nil || !st.Killed || st.RelayConnected || st.OutboxPending < 1 {
		t.Fatalf("status while killed = %+v, %v", st, err)
	}
	stop()

	// The switch survives a restart: no dial until Resume.
	d2 := d2NewDaemon(t, dir, relay)
	if !d2.Kill().Killed() {
		t.Fatal("kill switch lost across restart")
	}
	d2Run(t, d2)
	time.Sleep(100 * time.Millisecond)
	if relay.dials() != 1 {
		t.Fatalf("dialed while killed after restart: %d", relay.dials())
	}
	if err := d2.Kill().Resume(ctx, true); err != nil {
		t.Fatal(err)
	}
	d2Eventually(t, "dial after resume", func() bool { return relay.dials() == 2 })
	d2Eventually(t, "message queued while killed sent after resume", func() bool { return relay.box(1).sendCount() >= 1 })
}

func TestEnsureRegisteredWithInvite(t *testing.T) {
	ctx := context.Background()
	relay := &d2Relay{refuse: func(c transport.Credentials) error {
		if c.Invite == "" && c.AdminToken == "" {
			return transport.ErrRelayForbidden
		}
		return nil
	}}
	d := d2NewDaemon(t, t.TempDir(), relay)
	d2Run(t, d)
	d2Eventually(t, "first refused dial", func() bool { return relay.dials() >= 1 })
	if d.Registered() {
		t.Fatal("registered without credentials")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.EnsureRegistered(cctx, "INVITE-1"); err != nil {
		t.Fatalf("EnsureRegistered: %v", err)
	}
	if !d.Registered() {
		t.Fatal("not registered after EnsureRegistered")
	}
	last := relay.cred(relay.dials() - 1)
	if last.Invite != "INVITE-1" {
		t.Fatalf("dial creds = %+v", last)
	}
	if v, _, _ := d.Settings().GetSetting(ctx, SettingRelayInvite); v != "" {
		t.Fatalf("invite not cleared: %q", v)
	}
}

func TestEnsureRegisteredReportsRefusal(t *testing.T) {
	relay := &d2Relay{refuse: func(transport.Credentials) error { return transport.ErrRelayForbidden }}
	d := d2NewDaemon(t, t.TempDir(), relay)
	d2Run(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.EnsureRegistered(ctx, "USED-INVITE"); !errors.Is(err, transport.ErrRelayForbidden) {
		t.Fatalf("err = %v, want ErrRelayForbidden", err)
	}
}

func TestResetIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	relay := &d2Relay{}
	d := d2NewDaemon(t, dir, relay)
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	stop := d2Run(t, d)
	d2Eventually(t, "connection", func() bool { _, ok := d.Mailbox(); return ok })
	old := d.Identity().MachineID()

	if err := d.ResetIdentity(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !d.Kill().Killed() || d.Registered() {
		t.Fatalf("killed %v registered %v after reset", d.Kill().Killed(), d.Registered())
	}
	if d.Identity().MachineID() == old || d.Status() == nil {
		t.Fatal("identity not replaced")
	}
	if peers, _ := d.store.ListPeers(ctx); len(peers) != 0 {
		t.Fatalf("peers left after reset: %d", len(peers))
	}
	if _, err := d.store.CurrentPrekey(ctx); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("prekey left after reset: %v", err)
	}
	if pending, held, _ := d.store.CountOutbox(ctx); pending+held != 0 {
		t.Fatalf("outbox left after reset: %d/%d", pending, held)
	}
	st, err := d.Status().Status(ctx)
	if err != nil || st.MachineID != d.Identity().MachineID() {
		t.Fatalf("status after reset = %+v, %v", st, err)
	}
	newID := d.Identity().MachineID()
	stop()

	d2 := d2NewDaemon(t, dir, relay)
	defer d2.Close()
	if d2.Identity().MachineID() != newID {
		t.Fatal("new identity not persisted")
	}
}

func TestMaintainExpiresTasksAndClosesAbandonedSessions(t *testing.T) {
	ctx := context.Background()
	opts := d2Options(t.TempDir(), &d2Relay{})
	clock := core.NewFakeClock(d2Epoch)
	opts.Clock = clock
	d, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	sh, err := d.Shared().Share(ctx, 1, ShareRequest{Agent: "claude", ProjectDir: "/w/proj", Name: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	link := d2Link(t, d.store, peer.rec, sh.Session, "trainer", core.PermTasksAuto, core.PermMessages)
	queued := core.NewID()
	claimed := core.NewID()
	for _, id := range []string{queued, claimed} {
		env := d2Env(t, peer.rec, core.KindTaskCreate, link.ID, core.TaskCreateBody{TaskID: id, Instructions: "x"})
		if err := d.Tasks().HandleCreate(withLink(ctx, link), peer.rec, env); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Tasks().Claim(ctx, sh.Session.ID, claimed); err != nil {
		t.Fatal(err)
	}
	// The chat's connection ends: the session is away and keeps its link.
	if err := d.Shared().Detach(ctx, sh.Session.ID, 1); err != nil {
		t.Fatal(err)
	}
	clock.Advance(core.AwayGrace - time.Minute)
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if l, _ := d.store.GetLink(ctx, peer.rec.MachineID, link.ID); l.State != store.LinkActive {
		t.Fatalf("link closed inside the away grace: %+v", l)
	}
	// Past the grace the session closes, its link closes, its claimed task fails.
	clock.Advance(2 * time.Minute)
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := d.Shared().Get(ctx, sh.Session.ID); s.State != core.SessionClosed {
		t.Fatalf("session after the away grace = %s", s.State)
	}
	c, _ := d.store.GetTask(ctx, claimed)
	if c.State != core.TaskFailed || c.Notes[len(c.Notes)-1].Text != ReasonLinkClosed {
		t.Fatalf("claimed task %s %+v", c.State, c.Notes)
	}
	q, _ := d.store.GetTask(ctx, queued)
	if q.State != core.TaskFailed {
		t.Fatalf("queued task %s", q.State)
	}
	// Unclaimed tasks on a live link still expire after a day.
	sh2, _ := d.Shared().Share(ctx, 2, ShareRequest{Agent: "claude", ProjectDir: "/w/proj", Name: "second"})
	link2 := d2Link(t, d.store, peer.rec, sh2.Session, "trainer", core.PermTasksAuto, core.PermMessages)
	later := core.NewID()
	env := d2Env(t, peer.rec, core.KindTaskCreate, link2.ID, core.TaskCreateBody{TaskID: later, Instructions: "x"})
	if err := d.Tasks().HandleCreate(withLink(ctx, link2), peer.rec, env); err != nil {
		t.Fatal(err)
	}
	clock.Advance(core.UnclaimedExpiry + time.Minute)
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if tk, _ := d.store.GetTask(ctx, later); tk.State != core.TaskExpired {
		t.Fatalf("unclaimed task after a day = %s", tk.State)
	}
}

func TestSendToPausedPeers(t *testing.T) {
	ctx := context.Background()
	relay := &d2Relay{}
	d := d2NewDaemon(t, t.TempDir(), relay)
	away := newTestPeer(t, "away")
	away.rec.PausedByPeer = true
	mustPut(t, d.store, away.rec)
	muted := newTestPeer(t, "muted")
	muted.rec.Paused = true
	mustPut(t, d.store, muted.rec)
	d2Run(t, d)
	d2Eventually(t, "connection", func() bool { _, ok := d.Mailbox(); return ok })
	base := relay.box(0).sendCount()

	if _, err := d.Outbound().SendEnvelope(ctx, muted.rec.MachineID, core.KindChat, "", core.ChatBody{Text: "x"}); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("send to a peer we paused err = %v", err)
	}
	session := d2Share(t, d.Shared(), "lead")
	mutedLink := d2Link(t, d.store, muted.rec, session, "m", core.PermMessages, core.PermTasksAuto)
	awayLink := d2Link(t, d.store, away.rec, session, "a", core.PermMessages, core.PermTasksAuto)
	if _, err := d.Tasks().Create(ctx, session.ID, "/w", mutedLink.Num, "x", nil); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("task to a peer we paused err = %v", err)
	}
	if _, err := d.Outbound().SendEnvelope(ctx, away.rec.MachineID, core.KindChat, "", core.ChatBody{Text: "hi"}); err != nil {
		t.Fatalf("send to a peer that paused us: %v", err)
	}
	if _, err := d.Tasks().Create(ctx, session.ID, "/w", awayLink.Num, "later", nil); err != nil {
		t.Fatalf("task to a peer that paused us: %v", err)
	}
	if _, held, _ := d.store.CountOutbox(ctx); held != 2 {
		t.Fatalf("held = %d, want 2", held)
	}
	time.Sleep(50 * time.Millisecond)
	if relay.box(0).sendCount() != base {
		t.Fatal("held items were sent while the peer had us paused")
	}

	// control.resumed from the peer releases and delivers them.
	h, ok := d.svc.Load().registry.Lookup(core.KindControlResumed)
	if !ok {
		t.Fatal("no control.resumed handler")
	}
	resumed := d2Env(t, away.rec, core.KindControlResumed, "", core.EmptyBody{})
	if err := h.Handle(ctx, mustGetPeer(t, d.store, away.rec.MachineID), resumed); err != nil {
		t.Fatal(err)
	}
	d2Eventually(t, "held items delivered", func() bool { return relay.box(0).sendCount() >= base+2 })
	if _, held, _ := d.store.CountOutbox(ctx); held != 0 {
		t.Fatalf("held after resume = %d", held)
	}
}

func TestRetryableInboundFailureReconnectsForRedelivery(t *testing.T) {
	ctx := context.Background()
	relay := &d2Relay{}
	d := d2NewDaemon(t, t.TempDir(), relay)
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	var mu sync.Mutex
	calls := 0
	d.svc.Load().registry.Register(core.KindChat, HandlerFunc(func(context.Context, store.Peer, core.Envelope) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return Retryable(errors.New("disk busy"))
		}
		return nil
	}))
	d2Run(t, d)
	d2Eventually(t, "connection", func() bool { _, ok := d.Mailbox(); return ok })
	pk, err := d.svc.Load().prekeys.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env, err := core.NewEnvelope(core.SystemClock{}, peer.id.MachineID(), d.Identity().MachineID(), core.KindChat, core.ChatBody{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := sealing.Seal(peer.id, pk, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := fr.Marshal()
	delivery := transport.Delivery{Seq: 1, From: peer.id.Public(), ID: env.ID, Frame: raw}

	relay.box(0).deliveries <- delivery
	d2Eventually(t, "connection recycled after the retryable failure", func() bool {
		return relay.box(0).closed() && relay.dials() >= 2
	})
	relay.box(1).deliveries <- delivery
	d2Eventually(t, "redelivery handled", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls == 2
	})
}

func TestCloseAndResetStopPairing(t *testing.T) {
	ctx := context.Background()
	d := d2NewDaemon(t, t.TempDir(), &d2Relay{})
	old := d.Pairing()
	if err := d.ResetIdentity(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := old.Start(ctx, true); !errors.Is(err, ErrPairingClosed) {
		t.Fatalf("old pairing service after reset: %v", err)
	}
	cur := d.Pairing()
	d.Close()
	if _, _, err := cur.Start(ctx, true); !errors.Is(err, ErrPairingClosed) {
		t.Fatalf("pairing after Close: %v", err)
	}
}

// allowHookMailbox runs hook (once) when the daemon syncs the allow-list, the
// step between a successful dial and the mailbox going live.
type allowHookMailbox struct {
	*d2Mailbox
	once sync.Once
	hook func()
}

func (m *allowHookMailbox) Allow(ctx context.Context, ik ed25519.PublicKey) error {
	m.once.Do(m.hook)
	return m.d2Mailbox.Allow(ctx, ik)
}

type killDuringSyncRelay struct {
	d2Relay
	hook func()
}

func (r *killDuringSyncRelay) Dialer() transport.Dialer { return r }

func (r *killDuringSyncRelay) Dial(ctx context.Context, s transport.Signer, c transport.Credentials) (transport.Mailbox, error) {
	mb, err := r.d2Relay.Dial(ctx, s, c)
	if err != nil {
		return nil, err
	}
	return &allowHookMailbox{d2Mailbox: mb.(*d2Mailbox), hook: r.hook}, nil
}

// A kill that lands after the dial but before the mailbox goes live must
// still close it: the connection loop never serves a mailbox while killed.
func TestKillWhileConnectingClosesTheMailbox(t *testing.T) {
	ctx := context.Background()
	relay := &killDuringSyncRelay{}
	d := d2NewDaemon(t, t.TempDir(), relay)
	relay.hook = func() {
		if err := d.Kill().Kill(ctx); err != nil {
			t.Error(err)
		}
	}
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	d2Run(t, d)
	d2Eventually(t, "first dial", func() bool { return relay.dials() >= 1 })
	d2Eventually(t, "mailbox closed after the kill", relay.box(0).closed)
	d.mu.Lock()
	live := d.mb
	d.mu.Unlock()
	if live != nil {
		t.Fatal("mailbox recorded as live while killed")
	}
}

func TestMaintainPurgesOldFileRecords(t *testing.T) {
	ctx := context.Background()
	opts := d2Options(t.TempDir(), &d2Relay{})
	clock := core.NewFakeClock(d2Epoch)
	opts.Clock = clock
	d, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	onDisk := filepath.Join(t.TempDir(), "kept.bin")
	if err := os.WriteFile(onDisk, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.FileRecord{
		{FileID: "done", Direction: store.TaskInbound, Peer: "p", State: store.FileDone, LocalPath: onDisk, CreatedAt: d2Epoch},
		{FileID: "held", Direction: store.TaskInbound, Peer: "p", State: store.FileHeld, CreatedAt: d2Epoch},
	} {
		if err := d.store.PutFile(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(core.InboxRetention + time.Minute)
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.GetFile(ctx, "done"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old done record kept: %v", err)
	}
	if _, err := d.store.GetFile(ctx, "held"); err != nil {
		t.Fatalf("held record purged: %v", err)
	}
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("file on disk touched: %v", err)
	}
}

// gatedSendMailbox blocks Send while gate is set, until release is closed.
type gatedSendMailbox struct {
	*d2Mailbox
	gate    *atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *gatedSendMailbox) Send(ctx context.Context, to core.MachineID, id string, frame []byte) (transport.SendStatus, error) {
	if m.gate.Load() {
		m.once.Do(func() { close(m.entered) })
		<-m.release
	}
	return m.d2Mailbox.Send(ctx, to, id, frame)
}

type gatedSendRelay struct {
	d2Relay
	gate    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (r *gatedSendRelay) Dialer() transport.Dialer { return r }

func (r *gatedSendRelay) Dial(ctx context.Context, s transport.Signer, c transport.Credentials) (transport.Mailbox, error) {
	mb, err := r.d2Relay.Dial(ctx, s, c)
	if err != nil {
		return nil, err
	}
	return &gatedSendMailbox{d2Mailbox: mb.(*d2Mailbox), gate: &r.gate, entered: r.entered, release: r.release}, nil
}

// While the kill flush is still sending, the switch must already read as on
// (the IPC gate refuses agent calls) and inbound deliveries must not be
// handled; the flush itself still goes out on the live mailbox.
func TestKillFlushWindowIsClosed(t *testing.T) {
	ctx := context.Background()
	relay := &gatedSendRelay{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(relay.release) }) }
	d := d2NewDaemon(t, t.TempDir(), relay)
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	var mu sync.Mutex
	handled := 0
	d.svc.Load().registry.Register(core.KindChat, HandlerFunc(func(context.Context, store.Peer, core.Envelope) error {
		mu.Lock()
		defer mu.Unlock()
		handled++
		return nil
	}))
	d2Run(t, d)
	t.Cleanup(release) // runs before the stop registered by d2Run
	d2Eventually(t, "connection", func() bool { _, ok := d.Mailbox(); return ok })
	pk, err := d.svc.Load().prekeys.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}

	relay.gate.Store(true)
	if _, err := d.Outbound().SendEnvelope(ctx, peer.rec.MachineID, core.KindChat, "", core.ChatBody{Text: "slow"}); err != nil {
		t.Fatal(err)
	}
	<-relay.entered // the send loop is stuck in Send, so the kill flush waits
	killed := make(chan error, 1)
	go func() { killed <- d.Kill().Kill(ctx) }()
	d2Eventually(t, "switch reads as on during the flush", d.Kill().Killed)

	env, err := core.NewEnvelope(core.SystemClock{}, peer.id.MachineID(), d.Identity().MachineID(), core.KindChat, core.ChatBody{Text: "during flush"})
	if err != nil {
		t.Fatal(err)
	}
	fr, err := sealing.Seal(peer.id, pk, env)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := fr.Marshal()
	select {
	case relay.box(0).deliveries <- transport.Delivery{Seq: 1, From: peer.id.Public(), ID: env.ID, Frame: raw}:
	case <-time.After(5 * time.Second):
		t.Fatal("inbound not reading")
	}
	time.Sleep(50 * time.Millisecond)
	if relay.box(0).closed() {
		t.Fatal("mailbox closed while the kill flush was still sending")
	}
	release()
	if err := <-killed; err != nil {
		t.Fatal(err)
	}
	if relay.box(0).sendCount() < 1 {
		t.Fatal("flush did not send")
	}
	mu.Lock()
	defer mu.Unlock()
	if handled != 0 {
		t.Fatalf("inbound handled %d deliveries during the kill flush", handled)
	}
}

func TestResetIdentityWhenAlreadyKilled(t *testing.T) {
	ctx := context.Background()
	d := d2NewDaemon(t, t.TempDir(), &d2Relay{})
	defer d.Close()
	old := d.Identity().MachineID()
	if err := d.Kill().Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.ResetIdentity(ctx, true); err != nil {
		t.Fatal(err)
	}
	if d.Identity().MachineID() == old || !d.Kill().Killed() {
		t.Fatalf("identity replaced %v, killed %v", d.Identity().MachineID() != old, d.Kill().Killed())
	}
}
