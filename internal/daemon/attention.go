package daemon

import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/store"
)

// Counts is what is pending for a shared session, as numbers only.
type Counts struct {
	Unread   int // inbox items the session has not read (link notices included)
	Requests int // incoming link requests waiting for a human decision
}

// ChangeNotifier signals inbox changes. Implemented by *InboxService.
type ChangeNotifier interface {
	Changed() <-chan struct{}
}

// WakeSessions resolves wake tokens and sessions. Implemented by *SessionService.
type WakeSessions interface {
	ByWakeToken(ctx context.Context, token string) (store.SharedSession, error)
	Get(ctx context.Context, id string) (store.SharedSession, error)
}

// AttentionService tells a listener that something is pending for its
// session, and nothing else (v2 spec 3.2: the wake token reveals counts only).
type AttentionService struct {
	sessions WakeSessions
	inbox    store.InboxStore
	links    store.LinkStore
	changes  ChangeNotifier
}

// NewAttentionService wires the service.
func NewAttentionService(sessions WakeSessions, inbox store.InboxStore, links store.LinkStore, changes ChangeNotifier) *AttentionService {
	return &AttentionService{sessions: sessions, inbox: inbox, links: links, changes: changes}
}

// Counts returns the session's pending counts.
func (a *AttentionService) Counts(ctx context.Context, sessionID string) (Counts, error) {
	s, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return Counts{}, err
	}
	unread, _, err := a.inbox.SessionUnread(ctx, s.ID, s.Cursor)
	if err != nil {
		return Counts{}, err
	}
	reqs, err := a.links.ListLinks(ctx, store.LinkFilter{Session: s.ID, Direction: store.LinkInbound, States: []store.LinkState{store.LinkPending}})
	if err != nil {
		return Counts{}, err
	}
	return Counts{Unread: unread, Requests: len(reqs)}, nil
}

// Listen blocks until the session holding wakeToken has something pending,
// the timeout passes (<= 0: no timeout) or ctx ends. On timeout it returns
// zero counts and no error.
func (a *AttentionService) Listen(ctx context.Context, wakeToken string, timeout time.Duration) (Counts, error) {
	s, err := a.sessions.ByWakeToken(ctx, wakeToken)
	if err != nil {
		return Counts{}, err
	}
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	for {
		ch := a.changes.Changed() // taken before counting so a change in between is not missed
		c, err := a.Counts(ctx, s.ID)
		if err != nil || c.Unread+c.Requests > 0 {
			return c, err
		}
		select {
		case <-ch:
		case <-expired:
			return Counts{}, nil
		case <-ctx.Done():
			return Counts{}, ctx.Err()
		}
	}
}
