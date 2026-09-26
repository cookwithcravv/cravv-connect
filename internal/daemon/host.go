package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Audit event types for managed sessions.
const (
	EvManagedStart   = "managed_start"
	EvManagedClose   = "managed_close"
	EvManagedRun     = "managed_run"
	EvManagedRefused = "managed_refused"
)

// ErrManagedBusy means an offer is at its concurrency limit or the peer at
// its daily run cap (the request is rejected busy).
var ErrManagedBusy = errors.New("the offer is at its limit")

// HostOffers is what the SessionHost needs from the offer rules.
// Implemented by *OfferService.
type HostOffers interface {
	Get(ctx context.Context, id string) (store.Offer, error)
	Folders() FolderRules
}

// HostInbox reads a managed session's queue: its inbox, one item at a
// time. Implemented by *InboxService.
type HostInbox interface {
	Check(ctx context.Context, session string, limit int) ([]InboxEntry, error)
	Changed() <-chan struct{}
}

// HostTasks is what the SessionHost does to the tasks it runs.
// Implemented by *TaskService.
type HostTasks interface {
	Claim(ctx context.Context, session, id string) (store.Task, error)
	Get(ctx context.Context, session, id string) (store.Task, error)
	Fail(ctx context.Context, session, id, reason string) (store.Task, error)
	FailQueued(ctx context.Context, session, id, reason string) error
	FailClaimedBy(ctx context.Context, session, reason string) error
}

// HostDeps are the SessionHost collaborators. Tasks and Sender return the
// current services (ResetIdentity replaces them); Adapter is asked for each
// run, so a claude installed later is found.
type HostDeps struct {
	Offers   HostOffers
	Store    store.OfferStore
	Sessions *SessionService
	Inbox    HostInbox
	Links    LinkLookup
	Peers    store.PeerStore
	Tasks    func() HostTasks
	Sender   func() EnvelopeSender
	Adapter  func() AgentAdapter
	Runner   Runner
	RunDir   string          // where each run's MCP config is written (0700)
	Self     string          // the cravv-connect executable the MCP config starts
	StateDir string          // CRAVV_HOME for the run's MCP server
	Env      func() []string // the child's environment (default os.Environ)
	Killed   func() bool
	Clock    core.Clock
	Audit    audit.Logger
	Log      *slog.Logger
}

// SessionHost starts and runs managed sessions (v2 spec 6.2). A link
// request to an offer creates one (StartManaged); closing its one link
// closes it. Each managed session's inbox is its queue: one item at a time
// goes to a headless agent run in the offer's folder.
type SessionHost struct {
	d      HostDeps
	tokens runTokens
	poke   chan struct{}
	wg     sync.WaitGroup // workers

	startMu sync.Mutex // serializes StartManaged so the concurrency count is exact

	mu      sync.Mutex
	busy    map[string]bool               // sessions with a worker
	cancel  map[string]context.CancelFunc // sessions with a run: stops it
	groups  map[string]int                // sessions with a run: its process group
	notes   map[string][]string           // file notices for the next run
	live    map[string]int                // sessions a human opened: their queue waits
	noticed map[string]time.Time          // links last told of a refused message
	changed chan struct{}                 // closed and replaced when a worker stops
}

// NewSessionHost builds the host.
func NewSessionHost(d HostDeps) *SessionHost {
	if d.Audit == nil {
		d.Audit = audit.Nop{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Env == nil {
		d.Env = os.Environ
	}
	if d.Killed == nil {
		d.Killed = func() bool { return false }
	}
	return &SessionHost{
		d: d, tokens: runTokens{byHash: map[string]runGrant{}}, poke: make(chan struct{}, 1),
		busy: map[string]bool{}, cancel: map[string]context.CancelFunc{}, groups: map[string]int{}, notes: map[string][]string{},
		live: map[string]int{}, noticed: map[string]time.Time{}, changed: make(chan struct{}),
	}
}

// StartManaged creates the managed session for an accepted request from
// peer to offer offerID on link linkID, and returns it with the offer's
// permission. It fails with core.ErrNotFound for an offer the peer does
// not have, ErrBadFolder when the folder no longer passes its checks, and
// ErrManagedBusy at the offer's concurrency limit or the peer's daily cap.
func (h *SessionHost) StartManaged(ctx context.Context, peer store.Peer, offerID, linkID string) (store.SharedSession, core.Permission, error) {
	o, err := h.d.Offers.Get(ctx, offerID)
	if err != nil || o.Peer != peer.MachineID {
		return store.SharedSession{}, "", fmt.Errorf("offer: %w", core.ErrNotFound)
	}
	if err := h.d.Offers.Folders().Recheck(o); err != nil {
		return store.SharedSession{}, "", err
	}
	h.startMu.Lock()
	defer h.startMu.Unlock()
	open, err := h.openFor(ctx, o.ID)
	if err != nil {
		return store.SharedSession{}, "", err
	}
	if open >= o.MaxConcurrent {
		return store.SharedSession{}, "", fmt.Errorf("%d managed sessions open for %s: %w", open, o.Label, ErrManagedBusy)
	}
	now := h.d.Clock.Now()
	day, err := h.d.Store.CountRuns(ctx, store.RunFilter{Peer: peer.MachineID, Since: now.Add(-24 * time.Hour)})
	if err != nil {
		return store.SharedSession{}, "", err
	}
	if day >= o.RunsPerDay {
		return store.SharedSession{}, "", fmt.Errorf("%d runs today: %w", day, ErrManagedBusy)
	}
	sess, err := h.create(ctx, o)
	if err != nil {
		return store.SharedSession{}, "", err
	}
	m := store.ManagedSession{
		SessionID: sess.ID, OfferID: o.ID, Peer: peer.MachineID, LinkID: linkID, AgentSession: newUUID(),
		LastActive: now, CreatedAt: now,
	}
	if err := h.d.Store.PutManaged(ctx, m); err != nil {
		_ = h.d.Sessions.Close(ctx, sess.ID)
		return store.SharedSession{}, "", err
	}
	_ = h.d.Audit.Record(audit.Event{Type: EvManagedStart, Peer: peer.MachineID, Alias: peer.Alias, ItemID: sess.ID, Detail: map[string]any{
		"session": sess.Name, "offer": o.Label, "run_mode": string(o.RunMode),
	}})
	return sess, o.Permission, nil
}

// create makes the session, named from the label plus a 4-character suffix.
func (h *SessionHost) create(ctx context.Context, o store.Offer) (store.SharedSession, error) {
	var err error
	for range 5 {
		var sess store.SharedSession
		sess, err = h.d.Sessions.CreateManaged(ctx, o.Label+"-"+nameSuffix(), "managed session: "+o.Label, o.RealFolder)
		if !errors.Is(err, store.ErrNameTaken) {
			return sess, err
		}
	}
	return store.SharedSession{}, err
}

// openFor counts the offer's managed sessions that are not closed.
func (h *SessionHost) openFor(ctx context.Context, offerID string) (int, error) {
	all, err := h.d.Store.ListManaged(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range all {
		if m.OfferID != offerID {
			continue
		}
		if s, err := h.d.Sessions.Get(ctx, m.SessionID); err == nil && s.State != core.SessionClosed {
			n++
		}
	}
	return n, nil
}

// Close closes a managed session (and so its link). reason is audited.
func (h *SessionHost) Close(ctx context.Context, sessionID, reason string) error {
	m, err := h.d.Store.GetManaged(ctx, sessionID)
	if err != nil {
		return err
	}
	s, err := h.d.Sessions.Get(ctx, sessionID)
	if err != nil || s.State == core.SessionClosed {
		return err
	}
	h.stop(sessionID)
	if err := h.d.Sessions.Close(ctx, sessionID); err != nil {
		return err
	}
	_ = h.d.Audit.Record(audit.Event{Type: EvManagedClose, Peer: m.Peer, ItemID: sessionID, Detail: map[string]any{
		"session": s.Name, "reason": reason,
	}})
	return nil
}

// LinkClosed implements LinkCloseObserver: a managed session has exactly
// one link, and closing it closes the session.
func (h *SessionHost) LinkClosed(ctx context.Context, l store.Link) error {
	if _, err := h.d.Store.GetManaged(ctx, l.Session); err != nil {
		return nil
	}
	return h.Close(ctx, l.Session, "link closed: "+l.Reason)
}

// OfferRemoved implements OfferObserver: the offer's managed sessions close.
func (h *SessionHost) OfferRemoved(ctx context.Context, o store.Offer) {
	all, err := h.d.Store.ListManaged(ctx)
	if err != nil {
		h.d.Log.Warn("list managed sessions", "err", err)
		return
	}
	for _, m := range all {
		if m.OfferID == o.ID {
			if err := h.Close(ctx, m.SessionID, "offer removed"); err != nil {
				h.d.Log.Warn("close managed session", "err", err)
			}
		}
	}
}

// nameSuffix returns 4 random characters of [a-z0-9].
func nameSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("daemon: crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:])
}

// newUUID returns a random (version 4) UUID, the form claude --session-id takes.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("daemon: crypto/rand failed: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
