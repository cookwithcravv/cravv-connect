package daemon

import (
	"context"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// CreateManaged creates an open managed session (v2 spec 6.2) in folder.
// It is private (reached only through its link) and bound to no
// connection: a run token binds it for one run at a time (BindRun). A
// managed session never goes away; it closes.
func (s *SessionService) CreateManaged(ctx context.Context, name, purpose, folder string) (store.SharedSession, error) {
	vis := core.Visibility{Mode: core.VisibilityPrivate}
	if err := checkShareFields(name, purpose, vis); err != nil {
		return store.SharedSession{}, err
	}
	now := s.clock.Now()
	rec := store.SharedSession{
		ID: core.NewIDAt(s.clock), Name: name, Purpose: purpose, Kind: core.SessionManaged, Agent: core.ManagedAgentClaude,
		ProjectDir: folder, Visibility: vis, State: core.SessionOpen, CreatedAt: now, StateSince: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.PutShared(ctx, rec); err != nil {
		return store.SharedSession{}, err
	}
	return rec, nil
}

// BindRun binds managed session id to connection conn for one run (the
// SessionHost checked the run token). The session is then Current for conn
// until UnbindRun or Close. A connection that shares or runs another
// session is refused.
func (s *SessionService) BindRun(ctx context.Context, id string, conn uint64) (store.SharedSession, error) {
	rec, err := s.store.GetShared(ctx, id)
	if err != nil || rec.Kind != core.SessionManaged || rec.State == core.SessionClosed {
		return store.SharedSession{}, core.ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for sid, c := range s.bound {
		if c == conn && sid != id {
			return store.SharedSession{}, ErrAlreadyShared
		}
	}
	s.bound[id] = conn
	return rec, nil
}

// UnbindRun ends a run's binding: its connection can no longer act as the session.
func (s *SessionService) UnbindRun(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bound, id)
}
