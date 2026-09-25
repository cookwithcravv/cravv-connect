package daemon

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// In-memory store fakes for daemon unit tests. They follow the store.* contracts
// (core.ErrNotFound, store.ErrAliasTaken, Due ordering). They are separate types because
// PeerStore.DeletePeer and OutboxStore.DeletePeer share a name.

type memPeers struct {
	mu sync.Mutex
	m  map[core.MachineID]store.Peer
}

func newMemPeers() *memPeers { return &memPeers{m: map[core.MachineID]store.Peer{}} }

func (s *memPeers) PutPeer(_ context.Context, p store.Peer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, q := range s.m {
		if id != p.MachineID && q.Alias == p.Alias {
			return store.ErrAliasTaken
		}
	}
	s.m[p.MachineID] = p
	return nil
}

func (s *memPeers) GetPeer(_ context.Context, id core.MachineID) (store.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[id]
	if !ok {
		return store.Peer{}, core.ErrNotFound
	}
	return p, nil
}

func (s *memPeers) GetPeerByAlias(_ context.Context, alias string) (store.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.m {
		if p.Alias == alias {
			return p, nil
		}
	}
	return store.Peer{}, core.ErrNotFound
}

func (s *memPeers) ListPeers(_ context.Context) ([]store.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]store.Peer, 0, len(s.m))
	for _, p := range s.m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

func (s *memPeers) DeletePeer(_ context.Context, id core.MachineID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

type memPrekeys struct {
	mu sync.Mutex
	m  map[string]store.PrekeyRecord
}

func newMemPrekeys() *memPrekeys { return &memPrekeys{m: map[string]store.PrekeyRecord{}} }

func (s *memPrekeys) PutPrekey(_ context.Context, r store.PrekeyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[r.ID] = r
	return nil
}

func (s *memPrekeys) CurrentPrekey(_ context.Context) (store.PrekeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best store.PrekeyRecord
	found := false
	for _, r := range s.m {
		if r.SupersededAt == nil && (!found || r.CreatedAt.After(best.CreatedAt)) {
			best, found = r, true
		}
	}
	if !found {
		return store.PrekeyRecord{}, core.ErrNotFound
	}
	return best, nil
}

func (s *memPrekeys) GetPrekey(_ context.Context, id string) (store.PrekeyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[id]
	if !ok {
		return store.PrekeyRecord{}, core.ErrNotFound
	}
	return r, nil
}

func (s *memPrekeys) SupersedeAllExcept(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.m {
		if k != id && r.SupersededAt == nil {
			t := at
			r.SupersededAt = &t
			s.m[k] = r
		}
	}
	return nil
}

func (s *memPrekeys) DeleteSupersededBefore(_ context.Context, t time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, r := range s.m {
		if r.SupersededAt != nil && r.SupersededAt.Before(t) {
			delete(s.m, k)
			n++
		}
	}
	return n, nil
}

type memOutbox struct {
	mu sync.Mutex
	m  map[string]store.OutboxItem
}

func newMemOutbox() *memOutbox { return &memOutbox{m: map[string]store.OutboxItem{}} }

func (s *memOutbox) Enqueue(_ context.Context, it store.OutboxItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[it.ID] = it
	return nil
}

func (s *memOutbox) Due(_ context.Context, now time.Time, limit int) ([]store.OutboxItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.OutboxItem
	for _, it := range s.m {
		if it.Status == store.OutboxPending && !it.NextAttempt.After(now) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memOutbox) Get(_ context.Context, id string) (store.OutboxItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.m[id]
	if !ok {
		return store.OutboxItem{}, core.ErrNotFound
	}
	return it, nil
}

func (s *memOutbox) SetStatus(_ context.Context, id string, st store.OutboxStatus, attempts int, next time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.m[id]
	if !ok {
		return core.ErrNotFound
	}
	it.Status, it.Attempts, it.NextAttempt = st, attempts, next
	s.m[id] = it
	return nil
}

func (s *memOutbox) Delete(_ context.Context, ids ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.m, id)
	}
	return nil
}

func (s *memOutbox) HoldPeer(_ context.Context, to core.MachineID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, it := range s.m {
		if it.To == to && (it.Status == store.OutboxPending || it.Status == store.OutboxQueued) {
			it.Status = store.OutboxHeld
			s.m[id] = it
		}
	}
	return nil
}

func (s *memOutbox) ReleasePeer(_ context.Context, to core.MachineID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, it := range s.m {
		if it.To == to && it.Status == store.OutboxHeld {
			it.Status, it.NextAttempt = store.OutboxPending, now
			s.m[id] = it
		}
	}
	return nil
}

func (s *memOutbox) DeleteOutboxForPeer(_ context.Context, to core.MachineID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, it := range s.m {
		if it.To == to {
			delete(s.m, id)
		}
	}
	return nil
}

func (s *memOutbox) PurgeOutboxBefore(_ context.Context, t time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, it := range s.m {
		if it.CreatedAt.Before(t) {
			delete(s.m, id)
			n++
		}
	}
	return n, nil
}

func (s *memOutbox) item(id string) (store.OutboxItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.m[id]
	return it, ok
}

func (s *memOutbox) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

func (s *memOutbox) CountOutbox(_ context.Context) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, held := 0, 0
	for _, it := range s.m {
		if it.Status == store.OutboxHeld {
			held++
		} else {
			pending++
		}
	}
	return pending, held, nil
}

type memDedup struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newMemDedup() *memDedup { return &memDedup{m: map[string]time.Time{}} }

func (s *memDedup) SeenOrMark(_ context.Context, id string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[id]; ok {
		return true, nil
	}
	s.m[id] = at
	return false, nil
}

func (s *memDedup) PurgeDedupBefore(_ context.Context, t time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, at := range s.m {
		if at.Before(t) {
			delete(s.m, id)
			n++
		}
	}
	return n, nil
}
