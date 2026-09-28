package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/present"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// DefaultInboxLimit is used when Check or Wait is called with limit <= 0.
const DefaultInboxLimit = 50

// InboxEntry is an inbox item prepared for display. The API layer maps it to
// ipc.InboxView. Wrapped is the only field agents should read as content.
type InboxEntry struct {
	Item       store.InboxItem
	Alias      string // local alias of the sender (never a peer-chosen name)
	Link       int64  // local number of the link the item arrived on
	Permission string // what that link lets the sender do here
	Session    string // the sender's session name, from the link record
	Kind       string // view kind: chat | task | task_update | file | link
	FileID     string
	Path       string // local path of a downloaded file
	Wrapped    string // present.Wrap output
}

// InboxSessions reads and moves a shared session's read position.
// Implemented by *SessionService.
type InboxSessions interface {
	Get(ctx context.Context, id string) (store.SharedSession, error)
	SetCursor(ctx context.Context, id string, cursor int64) error
}

// ReadObserver is told which items a session's Check just returned (for
// the first time: the cursor moved past them). TaskService implements it to
// send task.update{seen}.
type ReadObserver interface {
	ItemsRead(ctx context.Context, session string, items []store.InboxItem)
}

// InboxService stores delivered items and serves them to the shared session
// each one is for (v2 spec 10: the inbox is scoped by session and link).
type InboxService struct {
	inbox     store.InboxStore
	sessions  InboxSessions
	links     LinkLookup
	peers     store.PeerStore
	clock     core.Clock
	renderers *RendererRegistry

	mu      sync.Mutex
	changed chan struct{} // closed and replaced by Notify
	readers []ReadObserver
}

// AddReadObserver registers o for items returned by Check.
func (s *InboxService) AddReadObserver(o ReadObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readers = append(s.readers, o)
}

// NewInboxService builds the service with the default renderers.
func NewInboxService(inbox store.InboxStore, sessions InboxSessions, links LinkLookup, peers store.PeerStore, clock core.Clock) *InboxService {
	return &InboxService{
		inbox:     inbox,
		sessions:  sessions,
		links:     links,
		peers:     peers,
		clock:     clock,
		renderers: DefaultRenderers(),
		changed:   make(chan struct{}),
	}
}

// Renderers exposes the renderer registry so new item kinds can plug in.
func (s *InboxService) Renderers() *RendererRegistry { return s.renderers }

// Notify wakes every Wait call.
func (s *InboxService) Notify() {
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

func (s *InboxService) waitChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// Changed returns a channel that is closed at the next Notify.
func (s *InboxService) Changed() <-chan struct{} { return s.waitChan() }

// Deliver stores an item for exactly one shared session (it.ToSession is
// its ID) and wakes waiters.
func (s *InboxService) Deliver(ctx context.Context, it store.InboxItem) (int64, error) {
	if it.ToSession == "" {
		return 0, errors.New("inbox: an item needs the shared session it is for")
	}
	if it.ReceivedAt.IsZero() {
		it.ReceivedAt = s.clock.Now()
	}
	seq, err := s.inbox.AddItem(ctx, it)
	if err != nil {
		return 0, err
	}
	s.Notify()
	return seq, nil
}

// LinkUnread counts the items the shared session has not read from one
// link, and their body bytes. A session that is gone has nothing unread.
func (s *InboxService) LinkUnread(ctx context.Context, session, linkID string) (int, int64, error) {
	rec, err := s.sessions.Get(ctx, session)
	if errors.Is(err, core.ErrNotFound) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return s.inbox.LinkUnread(ctx, rec.ID, linkID, rec.Cursor)
}

// Delivered reports whether an item with this message ID is in the inbox.
func (s *InboxService) Delivered(ctx context.Context, msgID string) (bool, error) {
	return s.inbox.HasInboxMsg(ctx, msgID)
}

// LinkClosed implements LinkCloseObserver: when the link's session is away,
// the items it has not read from that link are dropped (v2 spec 3.4). Their
// senders learn from link.closed; tasks fail with link_closed on both sides.
func (s *InboxService) LinkClosed(ctx context.Context, l store.Link) error {
	sess, err := s.sessions.Get(ctx, l.Session)
	if err != nil || sess.State != core.SessionAway {
		return nil
	}
	n, err := s.inbox.DeleteSessionItems(ctx, sess.ID, l.ID, sess.Cursor)
	if err == nil && n > 0 {
		s.Notify()
	}
	return err
}

// MaxInboxPageBytes caps one Check or Wait page: the JSON-encoded wrapped
// text of the items returned, plus a fixed allowance per item for the other
// fields. It keeps every inbox response well under the IPC line limit
// (ipc.MaxLineBytes, 8 MiB). A page always holds at least one item.
const MaxInboxPageBytes = 4 << 20

// inboxItemOverhead is the per-item allowance for fields besides Wrapped.
const inboxItemOverhead = 512

// Check returns up to limit unread items for the shared session, and at
// most MaxInboxPageBytes of them, and advances the session's cursor past
// the last one returned. Items that did not fit stay unread for the next
// call. If ctx is cancelled before the cursor moves (the client stopped
// waiting), nothing is marked and ctx.Err() is returned.
func (s *InboxService) Check(ctx context.Context, session string, limit int) ([]InboxEntry, error) {
	if limit <= 0 {
		limit = DefaultInboxLimit
	}
	rec, err := s.sessions.Get(ctx, session)
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.ErrNotShared
	}
	if err != nil {
		return nil, err
	}
	items, err := s.inbox.SessionItems(ctx, rec.ID, rec.Cursor, limit)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	out := make([]InboxEntry, 0, len(items))
	budget := MaxInboxPageBytes
	for _, it := range items {
		e := s.entry(ctx, it)
		cost := jsonStringLen(e.Wrapped) + inboxItemOverhead
		if len(out) > 0 && cost > budget {
			break
		}
		budget -= cost
		out = append(out, e)
	}
	// Two-phase: the page is built; move the cursor only if the caller is
	// still there to receive it.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.sessions.SetCursor(ctx, rec.ID, out[len(out)-1].Item.Seq); err != nil {
		return nil, err
	}
	read := make([]store.InboxItem, len(out))
	for i, e := range out {
		read[i] = e.Item
	}
	s.mu.Lock()
	obs := append([]ReadObserver(nil), s.readers...)
	s.mu.Unlock()
	for _, o := range obs {
		o.ItemsRead(ctx, rec.ID, read)
	}
	return out, nil
}

// jsonStringLen is the length of s encoded as a JSON string without HTML
// escaping: control characters cost up to 6 bytes each.
func jsonStringLen(s string) int {
	n := 2
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
			n += 2
		case c < 0x20:
			n += 6
		default:
			n++
		}
	}
	// U+2028 and U+2029 are escaped as \u2028 and \u2029 (3 bytes -> 6).
	n += 3 * (strings.Count(s, "\u2028") + strings.Count(s, "\u2029"))
	return n
}

// Wait blocks until the session has unread items or the timeout passes, then
// behaves like Check with DefaultInboxLimit. The timeout is capped at
// core.MaxWaitLong; <= 0 means core.MaxWait. It returns an empty slice (not
// an error) on timeout.
func (s *InboxService) Wait(ctx context.Context, session string, timeout time.Duration) ([]InboxEntry, error) {
	if timeout <= 0 {
		timeout = core.MaxWait
	}
	timeout = min(timeout, core.MaxWaitLong)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		ch := s.waitChan() // taken before checking so a Deliver in between is not missed
		entries, err := s.Check(ctx, session, DefaultInboxLimit)
		if err != nil || len(entries) > 0 {
			return entries, err
		}
		select {
		case <-ch:
		case <-timer.C:
			return []InboxEntry{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Unread counts the shared session's unread items per local alias.
func (s *InboxService) Unread(ctx context.Context, session string) (map[string]int, error) {
	rec, err := s.sessions.Get(ctx, session)
	if err != nil {
		return nil, err
	}
	_, byID, err := s.inbox.SessionUnread(ctx, rec.ID, rec.Cursor)
	if err != nil {
		return nil, err
	}
	byAlias := make(map[string]int, len(byID))
	for id, n := range byID {
		byAlias[s.alias(ctx, id)] += n
	}
	return byAlias, nil
}

func (s *InboxService) alias(ctx context.Context, id core.MachineID) string {
	if p, err := s.peers.GetPeer(ctx, id); err == nil {
		return p.Alias
	}
	return id.Short()
}

// entry renders an item. The link record, not the envelope, names the
// sender's session and says what the link permits.
func (s *InboxService) entry(ctx context.Context, it store.InboxItem) InboxEntry {
	e := InboxEntry{Item: it, Alias: s.alias(ctx, it.From), Session: it.FromSession}
	if l, err := s.links.GetLink(ctx, it.From, it.LinkID); err == nil {
		e.Link, e.Permission, e.Session = l.Num, string(l.PermissionIn), l.RemoteName
	}
	r := s.renderers.Render(it)
	body := r.Text
	if it.Note != "" {
		body = it.Note + "\n" + body
	}
	e.Kind, e.FileID, e.Path = r.ViewKind, r.FileID, r.Path
	e.Wrapped = present.Wrap(present.Item{
		Alias:      e.Alias,
		Session:    e.Session,
		Link:       e.Link,
		Permission: e.Permission,
		ID:         it.MsgID,
		Kind:       r.ViewKind,
		TaskID:     it.TaskID,
		Body:       body,
	})
	return e
}

// NewChatHandler stores incoming chat for core.KindChat behind a LinkGate:
// the item goes to the link's local session. A chat already in the inbox (a
// redelivery after a crash between the insert and the dedup mark) is not
// stored again; the store's unique chat index backs this up.
func NewChatHandler(inbox *InboxService) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
		l, ok := LinkFrom(ctx)
		if !ok {
			return errNoLink
		}
		body, err := decodeEnvBody[core.ChatBody](env.Body)
		if err != nil {
			return err
		}
		if len(body.Text) > core.MaxTextBytes {
			return fmt.Errorf("chat %s: %w", env.ID, core.ErrTooLarge)
		}
		if seen, err := inbox.Delivered(ctx, env.ID); err != nil {
			return Retryable(err)
		} else if seen {
			return nil
		}
		_, err = inbox.Deliver(ctx, store.InboxItem{
			MsgID:       env.ID,
			From:        peer.MachineID,
			FromSession: l.RemoteName,
			ToSession:   l.Session,
			LinkID:      l.ID,
			Kind:        core.KindChat,
			Body:        env.Body,
		})
		return Retryable(err)
	})
}
