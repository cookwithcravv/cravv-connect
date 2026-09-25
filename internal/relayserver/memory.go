package relayserver

import (
	"context"
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

type memQueue struct {
	lastSeq uint64
	frames  []QueuedFrame
	bytes   int64
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
	invites map[string]time.Time // token -> expiry
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
		invites: map[string]time.Time{},
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

func (m *memoryBackend) PutInvite(_ context.Context, token string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.invites[token]; ok {
		return ErrExists
	}
	m.invites[token] = m.clock.Now().Add(ttl)
	return nil
}

func (m *memoryBackend) ConsumeInvite(_ context.Context, token string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.invites[token]
	if !ok {
		return false, nil
	}
	delete(m.invites, token)
	return m.clock.Now().Before(exp), nil
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
		q.bytes -= int64(len(q.frames[i].Frame))
		i++
	}
	q.frames = q.frames[i:]
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
	q.frames = append(q.frames, QueuedFrame{
		Seq:      q.lastSeq,
		From:     append(ed25519.PublicKey(nil), from...),
		ID:       id,
		Frame:    append([]byte(nil), frame...),
		Enqueued: m.clock.Now(),
	})
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
		q.bytes -= int64(len(q.frames[i].Frame))
		i++
	}
	q.frames = q.frames[i:]
	return nil
}

func (m *memoryBackend) CreateRoom(_ context.Context, r RoomRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rooms[r.Nameplate]; ok {
		return ErrExists
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

func (m *memoryBackend) CreateBlob(_ context.Context, b BlobRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[b.ID]; ok {
		return ErrExists
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

func (m *memoryBackend) UploaderBytes(_ context.Context, uploader string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	var n int64
	for _, b := range m.blobs {
		if b.rec.Uploader == uploader && now.Before(b.rec.ExpiresAt) {
			n += b.rec.Size
		}
	}
	return n, nil
}
