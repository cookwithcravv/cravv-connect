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
