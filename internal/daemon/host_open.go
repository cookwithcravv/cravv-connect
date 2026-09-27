package daemon

import (
	"context"
	"fmt"
	"sync"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// EvManagedOpen is audited when a human opens a managed session.
const EvManagedOpen = "managed_open"

// ErrNotStarted means a managed session has no conversation to open yet.
var ErrNotStarted = fmt.Errorf("%w: this managed session has not run yet, so there is no conversation to open", core.ErrBadTransition)

// ManagedInfo is one open managed session as the owner sees it.
type ManagedInfo struct {
	Session store.SharedSession
	Managed store.ManagedSession
	Offer   store.Offer // zero when the offer was removed
	Alias   string      // the machine it runs for
	Link    int64       // its one link
	Running bool        // a run is in progress
	Live    bool        // a human has it open
}

// OpenInfo is how to open a managed session's conversation interactively.
type OpenInfo struct {
	Name    string
	Machine string // the machine whose peer drove the conversation (local alias)
	Folder  string
	Command []string // program and arguments, such as claude --resume <uuid>
}

// held reports whether a human holds the session's queue (Open).
func (h *SessionHost) held(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[id] > 0
}

// byName returns the open managed session called name.
func (h *SessionHost) byName(ctx context.Context, name string) (store.ManagedSession, store.SharedSession, error) {
	all, err := h.d.Store.ListManaged(ctx)
	if err != nil {
		return store.ManagedSession{}, store.SharedSession{}, err
	}
	for _, m := range all {
		s, err := h.d.Sessions.Get(ctx, m.SessionID)
		if err == nil && s.Name == name && s.State != core.SessionClosed {
			return m, s, nil
		}
	}
	return store.ManagedSession{}, store.SharedSession{}, fmt.Errorf("no open managed session %q: %w", name, core.ErrNotFound)
}

// List returns the open managed sessions, oldest first.
func (h *SessionHost) List(ctx context.Context) ([]ManagedInfo, error) {
	all, err := h.d.Store.ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	var out []ManagedInfo
	for _, m := range all {
		s, err := h.d.Sessions.Get(ctx, m.SessionID)
		if err != nil || s.State == core.SessionClosed {
			continue
		}
		info := ManagedInfo{Session: s, Managed: m, Alias: h.alias(ctx, m.Peer)}
		if o, err := h.d.Offers.Get(ctx, m.OfferID); err == nil {
			info.Offer = o
		}
		if l, err := h.d.Links.GetLink(ctx, m.Peer, m.LinkID); err == nil {
			info.Link = l.Num
		}
		h.mu.Lock()
		_, info.Running = h.cancel[m.SessionID]
		info.Live = h.live[m.SessionID] > 0
		h.mu.Unlock()
		out = append(out, info)
	}
	return out, nil
}

// CloseByName closes the open managed session called name (its link closes too).
func (h *SessionHost) CloseByName(ctx context.Context, name string) error {
	m, _, err := h.byName(ctx, name)
	if err != nil {
		return err
	}
	return h.Close(ctx, m.SessionID, "closed by the owner")
}

// Open holds the queue of the managed session called name for a human who
// resumes its conversation interactively (v2 spec 6.2): no new run starts,
// Open waits for a run in progress to end, and the session shows as live
// until release is called. It returns how to open the conversation.
func (h *SessionHost) Open(ctx context.Context, name string) (OpenInfo, func(), error) {
	m, s, err := h.byName(ctx, name)
	if err != nil {
		return OpenInfo{}, nil, err
	}
	if !m.Started {
		return OpenInfo{}, nil, ErrNotStarted
	}
	h.mu.Lock()
	h.live[s.ID]++
	h.mu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			h.mu.Lock()
			if h.live[s.ID]--; h.live[s.ID] <= 0 {
				delete(h.live, s.ID)
			}
			h.mu.Unlock()
			if cur, err := h.d.Store.GetManaged(context.Background(), s.ID); err == nil {
				cur.LastActive = h.d.Clock.Now()
				_ = h.d.Store.PutManaged(context.Background(), cur)
			}
			h.wake()
		})
	}
	for {
		h.mu.Lock()
		busy, ch := h.busy[s.ID], h.changed
		h.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-ch:
		case <-ctx.Done():
			release()
			return OpenInfo{}, nil, ctx.Err()
		}
	}
	adapter := h.d.Adapter()
	_ = h.d.Audit.Record(audit.Event{Type: EvManagedOpen, Peer: m.Peer, ItemID: s.ID, Detail: map[string]any{"session": s.Name}})
	return OpenInfo{Name: s.Name, Machine: h.alias(ctx, m.Peer), Folder: s.ProjectDir, Command: append([]string{adapter.Program()}, adapter.OpenArgs(m.AgentSession)...)}, release, nil
}
