package relayserver

import (
	"context"
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

type memQueue struct {
	lastSeq uint64
	frames  []QueuedFrame
	expires []time.Time // parallel to frames: Enqueued + the TTL given to Enqueue
	bytes   int64
}

type memInvite struct {
	inviter string
	expires time.Time
}

type memBlob struct {
	rec    BlobRecord
	chunks map[uint32][]byte
	stored int64
}

type memoryBackend struct {
	clock   core.Clock
	mu      sync.Mutex
	members map[string]bool
	invites map[string]memInvite
	allow   map[string]map[string]bool
	queues  map[string]*memQueue
	rooms   map[string]RoomRecord
	blobs   map[string]*memBlob
}

// NewMemoryBackend returns an in-memory Backend for tests and development.
// Pass the same clock as Config.Clock.
func NewMemoryBackend(clock core.Clock) Backend {
	if clock == nil {
		clock = core.SystemClock{}
	}
	return &memoryBackend{
		clock:   clock,
		members: map[string]bool{},
		invites: map[string]memInvite{},
		allow:   map[string]map[string]bool{},
		queues:  map[string]*memQueue{},
		rooms:   map[string]RoomRecord{},
		blobs:   map[string]*memBlob{},
	}
}

func (m *memoryBackend) IsMember(_ context.Context, mailbox string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.members[mailbox], nil
}

func (m *memoryBackend) AddMember(_ context.Context, mailbox string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.members[mailbox] = true
	return nil
}

func (m *memoryBackend) PutInvite(_ context.Context, token, inviter string, ttl time.Duration, maxOutstanding int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	if inv, ok := m.invites[token]; ok && now.Before(inv.expires) {
		return ErrExists
	}
	n := 0
	for _, inv := range m.invites {
		if inv.inviter == inviter && now.Before(inv.expires) {
			n++
		}
	}
	if n >= maxOutstanding {
		return ErrLimit
	}
	m.invites[token] = memInvite{inviter: inviter, expires: now.Add(ttl)}
	return nil
}

func (m *memoryBackend) RegisterWithInvite(_ context.Context, token, mailbox string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[token]
	if !ok {
		return false, nil
	}
	delete(m.invites, token)
	if !m.clock.Now().Before(inv.expires) {
		return false, nil
	}
	m.members[mailbox] = true
	return true, nil
}

func (m *memoryBackend) Allow(_ context.Context, mailbox, sender string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.allow[mailbox] == nil {
		m.allow[mailbox] = map[string]bool{}
	}
	m.allow[mailbox][sender] = true
	return nil
}

func (m *memoryBackend) Deny(_ context.Context, mailbox, sender string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.allow[mailbox], sender)
	return nil
}

func (m *memoryBackend) IsAllowed(_ context.Context, mailbox, sender string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allow[mailbox][sender], nil
}

func (m *memoryBackend) queue(mailbox string) *memQueue {
	q := m.queues[mailbox]
	if q == nil {
		q = &memQueue{}
		m.queues[mailbox] = q
	}
	return q
}

// dropExpired removes frames enqueued at or before now-ttl. Frames are in enqueue order.
func (m *memoryBackend) dropExpired(q *memQueue, ttl time.Duration) {
	cut := m.clock.Now().Add(-ttl)
	i := 0
	for i < len(q.frames) && !q.frames[i].Enqueued.After(cut) {
		i++
	}
	q.dropFront(i)
}

// dropFront removes the first n frames.
func (q *memQueue) dropFront(n int) {
	for _, f := range q.frames[:n] {
		q.bytes -= int64(len(f.Frame))
	}
	q.frames = q.frames[n:]
	q.expires = q.expires[n:]
}

func (m *memoryBackend) Enqueue(_ context.Context, mailbox string, from ed25519.PublicKey, id string, frame []byte, lim QueueLimits) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queue(mailbox)
	m.dropExpired(q, lim.TTL)
	if len(q.frames) >= lim.MaxFrames || q.bytes+int64(len(frame)) > lim.MaxBytes {
		return 0, ErrQueueFull
	}
	q.lastSeq++
	now := m.clock.Now()
	q.frames = append(q.frames, QueuedFrame{
		Seq:      q.lastSeq,
		From:     append(ed25519.PublicKey(nil), from...),
		ID:       id,
		Frame:    append([]byte(nil), frame...),
		Enqueued: now,
	})
	q.expires = append(q.expires, now.Add(lim.TTL))
	q.bytes += int64(len(frame))
	return q.lastSeq, nil
}

func (m *memoryBackend) Pending(_ context.Context, mailbox string, after uint64, ttl time.Duration, max int) ([]QueuedFrame, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queue(mailbox)
	m.dropExpired(q, ttl)
	var out []QueuedFrame
	for _, f := range q.frames {
		if f.Seq <= after {
			continue
		}
		out = append(out, f)
		if len(out) == max {
			break
		}
	}
	return out, nil
}

func (m *memoryBackend) Ack(_ context.Context, mailbox string, upTo uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queue(mailbox)
	i := 0
	for i < len(q.frames) && q.frames[i].Seq <= upTo {
		i++
	}
	q.dropFront(i)
	return nil
}

func (m *memoryBackend) CreateRoom(_ context.Context, r RoomRecord, maxPerOwner int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	if old, ok := m.rooms[r.Nameplate]; ok && now.Before(old.ExpiresAt) {
		return ErrExists
	}
	n := 0
	for _, x := range m.rooms {
		if x.Owner == r.Owner && now.Before(x.ExpiresAt) {
			n++
		}
	}
	if n >= maxPerOwner {
		return ErrLimit
	}
	m.rooms[r.Nameplate] = r
	return nil
}

func (m *memoryBackend) GetRoom(_ context.Context, nameplate string) (RoomRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[nameplate]
	if !ok {
		return RoomRecord{}, ErrNotFound
	}
	return r, nil
}

func (m *memoryBackend) ClaimJoin(_ context.Context, nameplate string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[nameplate]
	if !ok {
		return false, ErrNotFound
	}
	if r.Joined {
		return false, nil
	}
	r.Joined = true
	m.rooms[nameplate] = r
	return true, nil
}

func (m *memoryBackend) DeleteRoom(_ context.Context, nameplate string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rooms, nameplate)
	return nil
}

func (m *memoryBackend) CreateBlob(_ context.Context, b BlobRecord, quota, total int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[b.ID]; ok {
		return ErrExists
	}
	now := m.clock.Now()
	var mine, all int64
	for _, x := range m.blobs {
		if !now.Before(x.rec.ExpiresAt) {
			continue
		}
		all += x.rec.Size
		if x.rec.Uploader == b.Uploader {
			mine += x.rec.Size
		}
	}
	if mine+b.Size > quota {
		return ErrQuota
	}
	if all+b.Size > total {
		return ErrStorageFull
	}
	m.blobs[b.ID] = &memBlob{rec: b, chunks: map[uint32][]byte{}}
	return nil
}

func (m *memoryBackend) GetBlob(_ context.Context, id string) (BlobRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[id]
	if !ok {
		return BlobRecord{}, ErrNotFound
	}
	return b.rec, nil
}

func (m *memoryBackend) PutChunk(_ context.Context, id string, n uint32, data []byte, maxTotal int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[id]
	if !ok {
		return ErrNotFound
	}
	next := b.stored - int64(len(b.chunks[n])) + int64(len(data))
	if next > maxTotal {
		return ErrTooLarge
	}
	b.chunks[n] = append([]byte(nil), data...)
	b.stored = next
	return nil
}

func (m *memoryBackend) GetChunk(_ context.Context, id string, n uint32) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	c, ok := b.chunks[n]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), c...), nil
}

func (m *memoryBackend) DeleteBlob(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blobs, id)
	return nil
}

func (m *memoryBackend) PurgeExpired(_ context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for tok, inv := range m.invites {
		if !now.Before(inv.expires) {
			delete(m.invites, tok)
		}
	}
	for np, r := range m.rooms {
		if !now.Before(r.ExpiresAt) {
			delete(m.rooms, np)
		}
	}
	for id, b := range m.blobs {
		if !now.Before(b.rec.ExpiresAt) {
			delete(m.blobs, id)
		}
	}
	for _, q := range m.queues {
		keep := 0
		for i, f := range q.frames {
			if now.Before(q.expires[i]) {
				q.frames[keep], q.expires[keep] = f, q.expires[i]
				keep++
			} else {
				q.bytes -= int64(len(f.Frame))
			}
		}
		clear(q.frames[keep:])
		q.frames, q.expires = q.frames[:keep], q.expires[:keep]
	}
	return nil
}
