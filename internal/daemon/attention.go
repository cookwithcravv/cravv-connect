package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/cravv/cravv-connect/internal/store"
)

// Counts is what is pending for a shared session, as numbers plus local
// names only (aliases and link numbers): never bodies or peer-chosen text.
type Counts struct {
	Unread    int               // inbox items check_inbox has not returned yet
	Requests  int               // incoming link requests waiting for a human decision
	Approvals int               // tasks-ask tasks waiting for a human decision
	Groups    []present.Pending // the unread items per link and kind, oldest first
	LastSeq   int64             // the newest unread item (0 when none)
	Closed    bool              // the session is closed (Listen only)
}

// Unhandled counts the unread items that are not decision notices: what
// the Stop hook blocks on (v2 spec 7.1: never for approvals only).
func (c Counts) Unhandled() int {
	n := 0
	for _, g := range c.Groups {
		if !present.IsDecision(g.Kind) {
			n += g.Count
		}
	}
	return n
}

// ChangeNotifier signals inbox changes. Implemented by *InboxService.
type ChangeNotifier interface {
	Changed() <-chan struct{}
	Notify()
}

// WakeSessions resolves wake tokens and sessions. Implemented by *SessionService.
type WakeSessions interface {
	ByWakeToken(ctx context.Context, token string) (store.SharedSession, error)
	Get(ctx context.Context, id string) (store.SharedSession, error)
}

// AttentionDeps are the AttentionService collaborators.
type AttentionDeps struct {
	Sessions WakeSessions
	Inbox    store.InboxStore
	Links    store.LinkStore
	Tasks    store.TaskStore
	Peers    store.PeerStore
	Changes  ChangeNotifier
}

// AttentionService tells a listener that something is pending for its
// session, and nothing else (v2 spec 3.2: the wake token reveals counts only).
type AttentionService struct{ d AttentionDeps }

// NewAttentionService wires the service.
func NewAttentionService(d AttentionDeps) *AttentionService { return &AttentionService{d: d} }

// pendingKinds maps inbox item kinds to the kinds the listener names.
var pendingKinds = map[core.Kind]string{
	core.KindChat:         present.PendingMessage,
	core.KindTaskCreate:   present.PendingTask,
	core.KindTaskUpdate:   present.PendingTaskUpdate,
	core.KindFileOffer:    present.PendingFile,
	core.KindLinkRequest:  present.PendingRequest,
	core.KindLinkAccepted: present.PendingLink,
	core.KindLinkRejected: present.PendingLink,
	core.KindLinkClosed:   present.PendingLink,
	KindApprovalNotice:    present.PendingApproval,
}

// Counts returns the session's pending counts.
func (a *AttentionService) Counts(ctx context.Context, sessionID string) (Counts, error) {
	s, err := a.d.Sessions.Get(ctx, sessionID)
	if err != nil {
		return Counts{}, err
	}
	return a.counts(ctx, s)
}

func (a *AttentionService) counts(ctx context.Context, s store.SharedSession) (Counts, error) {
	groups, err := a.d.Inbox.SessionUnreadGroups(ctx, s.ID, s.Cursor)
	if err != nil {
		return Counts{}, err
	}
	var c Counts
	for _, g := range groups {
		c.Unread += g.Count
		c.LastSeq = max(c.LastSeq, g.MaxSeq)
		kind, ok := pendingKinds[g.Kind]
		if !ok {
			kind = string(g.Kind)
		}
		p := present.Pending{Machine: a.alias(ctx, g.Peer), Kind: kind, Count: g.Count}
		if l, err := a.d.Links.GetLink(ctx, g.Peer, g.LinkID); err == nil {
			p.Link = l.Num
		}
		c.Groups = append(c.Groups, p)
	}
	reqs, err := a.d.Links.ListLinks(ctx, store.LinkFilter{Session: s.ID, Direction: store.LinkInbound, States: []store.LinkState{store.LinkPending}})
	if err != nil {
		return Counts{}, err
	}
	c.Requests = len(reqs)
	held, err := a.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: []core.TaskState{core.TaskAwaitingApproval}})
	if err != nil {
		return Counts{}, err
	}
	for _, t := range held {
		if t.ToSession == s.ID {
			c.Approvals++
		}
	}
	return c, nil
}

func (a *AttentionService) alias(ctx context.Context, id core.MachineID) string {
	if p, err := a.d.Peers.GetPeer(ctx, id); err == nil {
		return p.Alias
	}
	return id.Short()
}

// Listen blocks until the session holding wakeToken has an unread item,
// the session closes (Counts.Closed), the timeout passes (<= 0: no
// timeout) or ctx ends. It wakes on new items only: a request or approval
// whose notice was already read does not wake it again, so a listener
// started again at once does not loop. On timeout it returns zero counts
// and no error.
func (a *AttentionService) Listen(ctx context.Context, wakeToken string, timeout time.Duration) (Counts, error) {
	s, err := a.d.Sessions.ByWakeToken(ctx, wakeToken)
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
		ch := a.d.Changes.Changed() // taken before counting so a change in between is not missed
		cur, err := a.d.Sessions.Get(ctx, s.ID)
		if errors.Is(err, core.ErrNotFound) || err == nil && cur.State == core.SessionClosed {
			return Counts{Closed: true}, nil
		}
		if err != nil {
			return Counts{}, err
		}
		c, err := a.counts(ctx, cur)
		if err != nil || c.Unread > 0 {
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

// SessionAway implements SessionObserver (nothing to do).
func (a *AttentionService) SessionAway(context.Context, store.SharedSession) {}

// SessionBack implements SessionObserver (nothing to do).
func (a *AttentionService) SessionBack(context.Context, store.SharedSession) {}

// SessionClosed implements SessionObserver: listeners of the session return.
func (a *AttentionService) SessionClosed(context.Context, store.SharedSession) { a.d.Changes.Notify() }
