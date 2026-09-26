package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Errors for invalid share and set requests (the API maps them to bad_request).
var (
	ErrBadSessionName = errors.New("invalid session name: use 1 to 32 characters of a-z, 0-9 and -, not starting with -")
	ErrBadPurpose     = errors.New("invalid purpose: one line of at most 120 characters")
	ErrBadVisibility  = errors.New("invalid visibility: use private, all-peers or peers:<alias>[,<alias>...]")
	ErrAlreadyShared  = errors.New("this connection already shares a session")
)

// SessionObserver is told when a shared session goes away, comes back or
// closes. LinkService implements it: it sends link.state and closes links.
type SessionObserver interface {
	SessionAway(ctx context.Context, s store.SharedSession)
	SessionBack(ctx context.Context, s store.SharedSession)
	SessionClosed(ctx context.Context, s store.SharedSession)
}

// ShareRequest is what session_share asks for. Agent and ProjectDir come
// from the connection's registration, never from the tool call.
type ShareRequest struct {
	Agent      string
	ProjectDir string
	Name       string
	Purpose    string
	Visibility core.Visibility // the zero value means private
}

// Shared is a newly shared session plus the two secrets for its client.
type Shared struct {
	Session       store.SharedSession
	WakeToken     string // lets a listener learn "something is pending" (counts only)
	ReattachToken string // lets the same agent and folder take the session over
}

// SessionService owns shared sessions (v2 spec 3.2): sharing, the binding of
// a session to one IPC connection, away and reattach, close, visibility.
// Bindings live in memory: a daemon restart ends every connection, so every
// session starts away and must be reattached.
type SessionService struct {
	store store.SharedSessionStore
	clock core.Clock

	mu        sync.Mutex
	bound     map[string]uint64 // session ID -> connection ID
	observers []SessionObserver
}

// NewSessionService builds the service.
func NewSessionService(st store.SharedSessionStore, clock core.Clock) *SessionService {
	return &SessionService{store: st, clock: clock, bound: map[string]uint64{}}
}

// AddObserver registers o for state changes, called in registration order.
func (s *SessionService) AddObserver(o SessionObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, o)
}

func (s *SessionService) notify(fn func(SessionObserver)) {
	s.mu.Lock()
	obs := append([]SessionObserver(nil), s.observers...)
	s.mu.Unlock()
	for _, o := range obs {
		fn(o)
	}
}

func checkShareFields(name, purpose string, vis core.Visibility) error {
	if !core.ValidSessionName(name) {
		return ErrBadSessionName
	}
	if !core.ValidPurpose(purpose) {
		return ErrBadPurpose
	}
	if !vis.Valid() {
		return ErrBadVisibility
	}
	return nil
}

// Share creates a live session bound to connection conn.
func (s *SessionService) Share(ctx context.Context, conn uint64, req ShareRequest) (Shared, error) {
	if req.Visibility.Mode == "" {
		req.Visibility = core.Visibility{Mode: core.VisibilityPrivate}
	}
	if err := checkShareFields(req.Name, req.Purpose, req.Visibility); err != nil {
		return Shared{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.bound {
		if c == conn {
			return Shared{}, ErrAlreadyShared
		}
	}
	wake, wakeHash := newToken()
	reattach, reattachHash := newToken()
	now := s.clock.Now()
	rec := store.SharedSession{
		ID: core.NewIDAt(s.clock), Name: req.Name, Purpose: req.Purpose, Kind: core.SessionLive,
		Agent: req.Agent, ProjectDir: req.ProjectDir, Visibility: req.Visibility, State: core.SessionOpen,
		ReattachHash: reattachHash, WakeHash: wakeHash, CreatedAt: now, StateSince: now,
	}
	if err := s.store.PutShared(ctx, rec); err != nil {
		return Shared{}, err
	}
	s.bound[rec.ID] = conn
	return Shared{Session: rec, WakeToken: wake, ReattachToken: reattach}, nil
}

// Get returns a session record.
func (s *SessionService) Get(ctx context.Context, id string) (store.SharedSession, error) {
	return s.store.GetShared(ctx, id)
}

// Current returns the session bound to connection conn. It fails with
// core.ErrNotShared when the session is closed or another connection
// reattached it.
func (s *SessionService) Current(ctx context.Context, id string, conn uint64) (store.SharedSession, error) {
	s.mu.Lock()
	c, ok := s.bound[id]
	s.mu.Unlock()
	if !ok || c != conn {
		return store.SharedSession{}, core.ErrNotShared
	}
	rec, err := s.store.GetShared(ctx, id)
	if err != nil || rec.State == core.SessionClosed {
		return store.SharedSession{}, core.ErrNotShared
	}
	return rec, nil
}

// Set changes the purpose and/or visibility (nil leaves a field unchanged).
func (s *SessionService) Set(ctx context.Context, id string, purpose *string, vis *core.Visibility) (store.SharedSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.store.GetShared(ctx, id)
	if err != nil {
		return rec, err
	}
	if rec.State == core.SessionClosed {
		return rec, core.ErrNotShared
	}
	if purpose != nil {
		rec.Purpose = *purpose
	}
	if vis != nil {
		rec.Visibility = *vis
	}
	if err := checkShareFields(rec.Name, rec.Purpose, rec.Visibility); err != nil {
		return rec, err
	}
	return rec, s.store.PutShared(ctx, rec)
}

// Close closes the session and, through the observers, all its links.
// Closing a closed session is a no-op.
func (s *SessionService) Close(ctx context.Context, id string) error {
	rec, changed, err := s.setState(ctx, id, core.SessionClosed, func(store.SharedSession) bool { return true })
	if err != nil || !changed {
		return err
	}
	s.mu.Lock()
	delete(s.bound, id)
	s.mu.Unlock()
	s.notify(func(o SessionObserver) { o.SessionClosed(ctx, rec) })
	return nil
}

// Detach handles the end of connection conn: its session goes away (links
// stay open for core.AwayGrace). A connection that lost the session to a
// reattach changes nothing.
func (s *SessionService) Detach(ctx context.Context, id string, conn uint64) error {
	s.mu.Lock()
	if c, ok := s.bound[id]; !ok || c != conn {
		s.mu.Unlock()
		return nil
	}
	delete(s.bound, id)
	s.mu.Unlock()
	rec, changed, err := s.setState(ctx, id, core.SessionAway, func(r store.SharedSession) bool { return r.State == core.SessionOpen })
	if err != nil || !changed {
		return err
	}
	s.notify(func(o SessionObserver) { o.SessionAway(ctx, rec) })
	return nil
}

// Reattach binds the session holding the reattach token to connection conn,
// taking it over from any other connection. The request must come from the
// same agent and project folder. Every failure looks the same (not found).
func (s *SessionService) Reattach(ctx context.Context, conn uint64, token, agent, projectDir string) (store.SharedSession, error) {
	rec, err := s.store.SharedByReattachHash(ctx, hashToken(token))
	if err != nil || rec.Agent != agent || rec.ProjectDir != projectDir {
		return store.SharedSession{}, fmt.Errorf("reattach: %w", core.ErrNotFound)
	}
	s.mu.Lock()
	for id, c := range s.bound {
		if c == conn && id != rec.ID {
			s.mu.Unlock()
			return store.SharedSession{}, ErrAlreadyShared
		}
	}
	s.bound[rec.ID] = conn
	s.mu.Unlock()
	back, changed, err := s.setState(ctx, rec.ID, core.SessionOpen, func(r store.SharedSession) bool { return r.State == core.SessionAway })
	if err != nil {
		return store.SharedSession{}, err
	}
	if changed {
		s.notify(func(o SessionObserver) { o.SessionBack(ctx, back) })
		return back, nil
	}
	return rec, nil
}

// ByWakeToken returns the live session a wake token belongs to.
func (s *SessionService) ByWakeToken(ctx context.Context, token string) (store.SharedSession, error) {
	rec, err := s.store.SharedByWakeHash(ctx, hashToken(token))
	if err != nil {
		return store.SharedSession{}, fmt.Errorf("wake token: %w", core.ErrNotFound)
	}
	return rec, nil
}

// AwayAll marks every open session away. The daemon calls it at startup:
// no connection survives a restart, and the away grace starts now.
func (s *SessionService) AwayAll(ctx context.Context) error {
	open, err := s.store.ListShared(ctx, core.SessionOpen)
	if err != nil {
		return err
	}
	for _, r := range open {
		rec, changed, err := s.setState(ctx, r.ID, core.SessionAway, func(r store.SharedSession) bool { return r.State == core.SessionOpen })
		if err != nil {
			return err
		}
		if changed {
			s.notify(func(o SessionObserver) { o.SessionAway(ctx, rec) })
		}
	}
	return nil
}

// Sweep closes sessions that stayed away longer than core.AwayGrace.
func (s *SessionService) Sweep(ctx context.Context) (int, error) {
	away, err := s.store.ListShared(ctx, core.SessionAway)
	if err != nil {
		return 0, err
	}
	now := s.clock.Now()
	n := 0
	for _, r := range away {
		if now.Sub(r.StateSince) <= core.AwayGrace {
			continue
		}
		if err := s.Close(ctx, r.ID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// List returns the sessions in any of states (all when none).
func (s *SessionService) List(ctx context.Context, states ...core.SessionState) ([]store.SharedSession, error) {
	return s.store.ListShared(ctx, states...)
}

// Visible returns the open and away sessions the peer may see (v2 spec 3.3).
func (s *SessionService) Visible(ctx context.Context, peer core.MachineID) ([]store.SharedSession, error) {
	live, err := s.store.ListShared(ctx, core.SessionOpen, core.SessionAway)
	if err != nil {
		return nil, err
	}
	var out []store.SharedSession
	for _, r := range live {
		if r.Visibility.Includes(peer) {
			out = append(out, r)
		}
	}
	return out, nil
}

// VisibleTo returns the session id when it is open or away and visible to
// the peer. A session the peer cannot see fails exactly like a missing one.
func (s *SessionService) VisibleTo(ctx context.Context, id string, peer core.MachineID) (store.SharedSession, error) {
	rec, err := s.store.GetShared(ctx, id)
	if err != nil || rec.State == core.SessionClosed || !rec.Visibility.Includes(peer) {
		return store.SharedSession{}, core.ErrNotFound
	}
	return rec, nil
}

// ForProjectDir returns an open session shared from projectDir (hooks use it).
func (s *SessionService) ForProjectDir(ctx context.Context, projectDir string) (store.SharedSession, bool) {
	open, err := s.store.ListShared(ctx, core.SessionOpen)
	if err != nil {
		return store.SharedSession{}, false
	}
	for _, r := range open {
		if r.ProjectDir == projectDir {
			return r, true
		}
	}
	return store.SharedSession{}, false
}

// SetCursor stores the session's inbox read position.
func (s *SessionService) SetCursor(ctx context.Context, id string, cursor int64) error {
	return s.store.SetSharedCursor(ctx, id, cursor)
}

// PurgeClosed deletes closed sessions older than core.InboxRetention.
func (s *SessionService) PurgeClosed(ctx context.Context) (int, error) {
	return s.store.PurgeClosedShared(ctx, s.clock.Now().Add(-core.InboxRetention))
}

// setState moves a session to state when cond holds for its current record.
// It reports whether the state changed.
func (s *SessionService) setState(ctx context.Context, id string, state core.SessionState, cond func(store.SharedSession) bool) (store.SharedSession, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.store.GetShared(ctx, id)
	if err != nil {
		return rec, false, err
	}
	if rec.State == state || rec.State == core.SessionClosed || !cond(rec) {
		return rec, false, nil
	}
	rec.State = state
	rec.StateSince = s.clock.Now()
	if err := s.store.PutShared(ctx, rec); err != nil {
		return rec, false, err
	}
	return rec, true, nil
}
