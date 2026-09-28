package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// ErrInboundFlood is returned for an item a link sent over its inbound
// limits. It is not retryable: Inbound confirms the item anyway, so the
// sender stops resending it.
var ErrInboundFlood = errors.New("the link is over its inbound limit: item dropped")

const (
	// inboundDropWarnFor is how long status keeps warning about a link after
	// its last dropped item.
	inboundDropWarnFor = time.Hour
	// inboundDropAuditEvery bounds the audit entries per link.
	inboundDropAuditEvery = time.Minute
	// inboundBucketsPrune is how many links the limiter tracks before it
	// drops the buckets that are full again.
	inboundBucketsPrune = 1024
)

// InboundLimits are the per-link inbound limits.
type InboundLimits struct {
	PerMinute   int   // sustained items per minute
	Burst       int   // items at once
	UnreadItems int   // items the link's session has not read yet
	UnreadBytes int64 // bytes of those items
}

// DefaultInboundLimits are the core limits.
func DefaultInboundLimits() InboundLimits {
	return InboundLimits{PerMinute: core.InboundItemsPerMinute, Burst: core.InboundItemsBurst,
		UnreadItems: core.MaxUnreadItemsPerLink, UnreadBytes: core.MaxUnreadBytesPerLink}
}

// LinkUnreadCounter counts what a session has not read from one link.
// Implemented by *InboxService.
type LinkUnreadCounter interface {
	LinkUnread(ctx context.Context, session, linkID string) (items int, bytes int64, err error)
}

// InboundLimiter caps what one link may add to this machine's inbox (chat
// and task.update, up to 64 KiB each and kept 30 days): a token bucket and
// a budget of unread items and bytes. An item over either is dropped with
// ErrInboundFlood, audited (at most once per link per minute) and counted
// for a status warning.
type InboundLimiter struct {
	unread LinkUnreadCounter
	limits InboundLimits
	clock  core.Clock
	audit  audit.Logger

	mu      sync.Mutex
	buckets map[string]*tokenBucket // by link ID
	drops   map[string]*linkDrops   // by link ID
}

type tokenBucket struct {
	tokens float64
	at     time.Time
}

type linkDrops struct {
	num       int64
	alias     string
	total     int64
	unaudited int64
	last      time.Time
	audited   time.Time
}

// NewInboundLimiter builds a limiter; lg may be nil.
func NewInboundLimiter(unread LinkUnreadCounter, limits InboundLimits, clock core.Clock, lg audit.Logger) *InboundLimiter {
	if lg == nil {
		lg = audit.Nop{}
	}
	return &InboundLimiter{unread: unread, limits: limits, clock: clock, audit: lg,
		buckets: map[string]*tokenBucket{}, drops: map[string]*linkDrops{}}
}

// Wrap puts h behind the limiter. It must run inside the LinkGate, which
// puts the link in ctx.
func (l *InboundLimiter) Wrap(h Handler) Handler {
	return HandlerFunc(func(ctx context.Context, peer store.Peer, env core.Envelope) error {
		link, ok := LinkFrom(ctx)
		if !ok {
			return errNoLink
		}
		if err := l.admit(ctx, peer, link, len(env.Body)); err != nil {
			return err
		}
		return h.Handle(ctx, peer, env)
	})
}

// admit takes a token for an item of size bytes on link, or refuses it.
func (l *InboundLimiter) admit(ctx context.Context, peer store.Peer, link store.Link, size int) error {
	items, bytes, err := l.unread.LinkUnread(ctx, link.Session, link.ID)
	if err != nil {
		return Retryable(err)
	}
	l.mu.Lock()
	now := l.clock.Now()
	b := l.bucket(link.ID, now)
	var reason string
	switch {
	case b.tokens < 1:
		reason = "rate"
	case items >= l.limits.UnreadItems || bytes+int64(size) > l.limits.UnreadBytes:
		reason = "unread"
	default:
		b.tokens--
		l.mu.Unlock()
		return nil
	}
	ev, record := l.dropLocked(peer, link, reason, now)
	l.mu.Unlock()
	if record {
		_ = l.audit.Record(ev)
	}
	return fmt.Errorf("%w (%s)", ErrInboundFlood, reason)
}

// bucket returns the link's bucket refilled to now.
func (l *InboundLimiter) bucket(linkID string, now time.Time) *tokenBucket {
	burst := float64(l.limits.Burst)
	b, ok := l.buckets[linkID]
	if !ok {
		if len(l.buckets) >= inboundBucketsPrune {
			l.pruneLocked(now)
		}
		b = &tokenBucket{tokens: burst, at: now}
		l.buckets[linkID] = b
		return b
	}
	if elapsed := now.Sub(b.at); elapsed > 0 {
		b.tokens = min(burst, b.tokens+elapsed.Minutes()*float64(l.limits.PerMinute))
		b.at = now
	}
	return b
}

// pruneLocked drops the buckets that would be full again (a new bucket
// starts full, so nothing changes) and warnings that have run out.
func (l *InboundLimiter) pruneLocked(now time.Time) {
	burst := float64(l.limits.Burst)
	for id, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Minutes()*float64(l.limits.PerMinute) >= burst {
			delete(l.buckets, id)
		}
	}
	for id, d := range l.drops {
		if now.Sub(d.last) >= inboundDropWarnFor {
			delete(l.drops, id)
		}
	}
}

// dropLocked counts a dropped item and returns the audit event to record,
// if one is due.
func (l *InboundLimiter) dropLocked(peer store.Peer, link store.Link, reason string, now time.Time) (audit.Event, bool) {
	d, ok := l.drops[link.ID]
	if !ok || now.Sub(d.last) >= inboundDropWarnFor {
		d = &linkDrops{}
		l.drops[link.ID] = d
	}
	d.num, d.alias, d.last = link.Num, peer.Alias, now
	d.total++
	d.unaudited++
	if !d.audited.IsZero() && now.Sub(d.audited) < inboundDropAuditEvery {
		return audit.Event{}, false
	}
	ev := audit.Event{Type: audit.EvInboundDropped, Peer: peer.MachineID, Alias: peer.Alias,
		Detail: map[string]any{"link": link.Num, "session": link.Session, "reason": reason, "dropped": d.unaudited}}
	d.audited, d.unaudited = now, 0
	return ev, true
}

// Warnings lists the links that had items dropped in the last hour (for status).
func (l *InboundLimiter) Warnings() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	var list []*linkDrops
	for _, d := range l.drops {
		if now.Sub(d.last) < inboundDropWarnFor {
			list = append(list, d)
		}
	}
	slices.SortFunc(list, func(a, b *linkDrops) int { return int(a.num - b.num) })
	out := make([]string, 0, len(list))
	for _, d := range list {
		out = append(out, fmt.Sprintf("link %d: %s is sending too fast, dropped %d messages", d.num, d.alias, d.total))
	}
	return out
}
