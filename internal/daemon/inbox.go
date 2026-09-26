package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/cravv/cravv-connect/internal/store"
)

// DefaultInboxLimit is used when Check or Wait is called with limit <= 0.
const DefaultInboxLimit = 50

// InboxEntry is an inbox item prepared for display. The API layer maps it to
// ipc.InboxView. Wrapped is the only field agents should read as content.
type InboxEntry struct {
	Item    store.InboxItem
	Alias   string // local alias of the sender (never a peer-chosen name)
	Trust   string // trust level this machine gives the sender
	Kind    string // view kind: chat | task | task_update | file
	FileID  string
	Path    string // local path of a downloaded file
	Wrapped string // present.Wrap output
}

// InboxService stores delivered items and serves them per session (spec 8.2).
type InboxService struct {
	inbox     store.InboxStore
	sessions  *SessionRegistry
	peers     store.PeerStore
	clock     core.Clock
	renderers *RendererRegistry

	mu      sync.Mutex
	changed chan struct{} // closed and replaced by Notify
}

// NewInboxService builds the service with the default renderers.
func NewInboxService(inbox store.InboxStore, sessions *SessionRegistry, peers store.PeerStore, clock core.Clock) *InboxService {
	return &InboxService{
		inbox:     inbox,
		sessions:  sessions,
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

// OriginallyFor is the note attached to a session message that became machine-wide.
func OriginallyFor(session string) string {
	return fmt.Sprintf("(originally for %s)", session)
}

// Deliver stores an item and wakes waiters. An item addressed to a session
// that is unknown (never registered or already expired) becomes machine-wide
// with an "(originally for <session>)" note.
func (s *InboxService) Deliver(ctx context.Context, it store.InboxItem) (int64, error) {
	if it.ToSession != "" && !s.sessions.Exists(ctx, it.ToSession) {
		it.Note = OriginallyFor(it.ToSession)
		it.ToSession = ""
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

// DeliverToSession stores an item for exactly one shared session
// (it.ToSession is its ID) and wakes waiters. Unlike Deliver it never turns
// the item into a machine-wide one.
func (s *InboxService) DeliverToSession(ctx context.Context, it store.InboxItem) (int64, error) {
	if it.ToSession == "" {
		return 0, errors.New("inbox: an item for a shared session needs its ID")
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

// Delivered reports whether an item with this message ID is in the inbox.
func (s *InboxService) Delivered(ctx context.Context, msgID string) (bool, error) {
	return s.inbox.HasInboxMsg(ctx, msgID)
}

// RedirectOrphans makes an expired session's unread items machine-wide.
func (s *InboxService) RedirectOrphans(ctx context.Context, session string) error {
	n, err := s.inbox.RedirectOrphans(ctx, session, OriginallyFor(session))
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

// Check returns up to limit unread items for the session, and at most
// MaxInboxPageBytes of them, marks them read and advances the session's
// cursor past the last one returned. Items that did not fit stay unread for
// the next call. If ctx is cancelled before the items are marked read
// (the client stopped waiting), nothing is marked and ctx.Err() is returned.
func (s *InboxService) Check(ctx context.Context, session string, limit int) ([]InboxEntry, error) {
	if limit <= 0 {
		limit = DefaultInboxLimit
	}
	rec, err := s.sessions.Get(ctx, session)
	if err != nil {
		return nil, err
	}
	items, err := s.inbox.ItemsFor(ctx, session, rec.Cursor, limit)
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
	seqs := make([]int64, len(out))
	for i, e := range out {
		seqs[i] = e.Item.Seq
	}
	// Two-phase: the page is built; mark it read only if the caller is still
	// there to receive it.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.inbox.MarkRead(ctx, seqs); err != nil {
		return nil, err
	}
	if err := s.sessions.SetCursor(ctx, session, seqs[len(seqs)-1]); err != nil {
		return nil, err
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
// core.MaxWait; <= 0 means core.MaxWait. It returns an empty slice (not an
// error) on timeout.
func (s *InboxService) Wait(ctx context.Context, session string, timeout time.Duration) ([]InboxEntry, error) {
	if timeout <= 0 || timeout > core.MaxWait {
		timeout = core.MaxWait
	}
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

// unreadPage is how many items Unread("") scans per store query.
const unreadPage = 500

// Unread counts unread items per local alias. For a session it counts what
// that session has not read yet. For session "" it counts machine-wide items
// that no session has read (hooks in a folder without a session use this).
func (s *InboxService) Unread(ctx context.Context, session string) (map[string]int, error) {
	byID := map[core.MachineID]int{}
	if session != "" {
		rec, err := s.sessions.Get(ctx, session)
		if err != nil {
			return nil, err
		}
		_, counts, err := s.inbox.UnreadCount(ctx, session, rec.Cursor)
		if err != nil {
			return nil, err
		}
		byID = counts
	} else {
		var after int64
		for {
			items, err := s.inbox.ItemsFor(ctx, "", after, unreadPage)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				if !it.ReadByAny {
					byID[it.From]++
				}
			}
			if len(items) < unreadPage {
				break
			}
			after = items[len(items)-1].Seq
		}
	}
	byAlias := make(map[string]int, len(byID))
	for id, n := range byID {
		alias, _ := s.aliasTrust(ctx, id)
		byAlias[alias] += n
	}
	return byAlias, nil
}

func (s *InboxService) aliasTrust(ctx context.Context, id core.MachineID) (string, string) {
	p, err := s.peers.GetPeer(ctx, id)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return id.Short(), "unpaired"
		}
		return id.Short(), "unknown"
	}
	return p.Alias, p.TrustIn.String()
}

func (s *InboxService) entry(ctx context.Context, it store.InboxItem) InboxEntry {
	alias, trust := s.aliasTrust(ctx, it.From)
	r := s.renderers.Render(it)
	body := r.Text
	if it.Note != "" {
		body = it.Note + "\n" + body
	}
	return InboxEntry{
		Item:   it,
		Alias:  alias,
		Trust:  trust,
		Kind:   r.ViewKind,
		FileID: r.FileID,
		Path:   r.Path,
		Wrapped: present.Wrap(present.Item{
			Alias:   alias,
			Session: it.FromSession,
			Trust:   trust,
			ID:      it.MsgID,
			Kind:    r.ViewKind,
			TaskID:  it.TaskID,
			Body:    body,
		}),
	}
}

// NewChatHandler stores incoming chat for core.KindChat. A chat already in the
// inbox (a redelivery after a crash between the insert and the dedup mark) is
// not stored again; the store's unique chat index backs this up.
func NewChatHandler(inbox *InboxService) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
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
			FromSession: env.FromSession,
			ToSession:   env.ToSession,
			Kind:        core.KindChat,
			Body:        env.Body,
		})
		return Retryable(err)
	})
}
