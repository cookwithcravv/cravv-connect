package relayserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"
)

// Backend errors.
var (
	ErrNotFound  = errors.New("relayserver: not found")
	ErrExists    = errors.New("relayserver: already exists")
	ErrQueueFull = errors.New("relayserver: queue full")
	ErrTooLarge  = errors.New("relayserver: too large")
	// ErrLimit means a per-member count cap (live invites, live rooms) is reached.
	ErrLimit = errors.New("relayserver: per-member limit reached")
	// ErrQuota means the uploader's unexpired blobs would exceed its quota.
	ErrQuota = errors.New("relayserver: blob quota exceeded")
	// ErrStorageFull means all unexpired blobs on the relay would exceed the relay-wide cap.
	ErrStorageFull = errors.New("relayserver: relay blob storage full")
)

// Registry holds relay membership and invites. Mailbox IDs are relayproto.MailboxID values.
type Registry interface {
	IsMember(ctx context.Context, mailbox string) (bool, error)
	AddMember(ctx context.Context, mailbox string) error
	// PutInvite stores a single-use invite minted by inviter. It returns ErrLimit when
	// inviter already has maxOutstanding unused, unexpired invites, and ErrExists when
	// the token is taken. The check and the insert are atomic.
	PutInvite(ctx context.Context, token, inviter string, ttl time.Duration, maxOutstanding int) error
	// RegisterWithInvite consumes the invite and adds mailbox as a member in one atomic
	// step. False (and no change) when the invite is unknown, used, or expired.
	RegisterWithInvite(ctx context.Context, token, mailbox string) (bool, error)
}

// AllowLists holds, per mailbox, the sender mailboxes allowed to queue frames into it.
type AllowLists interface {
	Allow(ctx context.Context, mailbox, sender string) error
	Deny(ctx context.Context, mailbox, sender string) error
	IsAllowed(ctx context.Context, mailbox, sender string) (bool, error)
}

// QueuedFrame is one frame waiting in a mailbox queue.
type QueuedFrame struct {
	Seq      uint64
	From     ed25519.PublicKey
	ID       string
	Frame    []byte
	Enqueued time.Time
}

// QueueLimits caps one mailbox queue.
type QueueLimits struct {
	MaxBytes  int64
	MaxFrames int
	TTL       time.Duration
}

// Queues holds mailbox queues. Seq is per mailbox, starts at 1, increases by one per
// accepted frame, and is never reused, even after the queue empties.
type Queues interface {
	// Enqueue drops expired frames, checks caps (ErrQueueFull), and appends with the next seq.
	Enqueue(ctx context.Context, mailbox string, from ed25519.PublicKey, id string, frame []byte, lim QueueLimits) (uint64, error)
	// Pending returns up to max unacked, unexpired frames with Seq > after, ascending.
	Pending(ctx context.Context, mailbox string, after uint64, ttl time.Duration, max int) ([]QueuedFrame, error)
	// Ack removes every frame with Seq <= upTo.
	Ack(ctx context.Context, mailbox string, upTo uint64) error
}

// RoomRecord is the persistent part of a pairing room.
type RoomRecord struct {
	Nameplate    string
	CreatorToken string
	Owner        string // creator mailbox
	ExpiresAt    time.Time
	Joined       bool
}

// RoomStore holds pairing room records.
type RoomStore interface {
	// CreateRoom stores r. A nameplate whose room has expired counts as free. It returns
	// ErrExists if a live room holds the nameplate and ErrLimit if r.Owner already has
	// maxPerOwner live rooms. The checks and the insert are atomic.
	CreateRoom(ctx context.Context, r RoomRecord, maxPerOwner int) error
	GetRoom(ctx context.Context, nameplate string) (RoomRecord, error)
	// ClaimJoin atomically sets Joined. False when it was already set.
	ClaimJoin(ctx context.Context, nameplate string) (bool, error)
	DeleteRoom(ctx context.Context, nameplate string) error
}

// BlobRecord describes one uploaded file.
type BlobRecord struct {
	ID        string
	Uploader  string // mailbox
	Recipient string // mailbox
	Size      int64
	Chunks    uint32
	ExpiresAt time.Time
}

// Blobs holds blob records and chunk bytes.
type Blobs interface {
	// CreateBlob stores b if, counting unexpired blobs only, the uploader's declared
	// sizes stay within quota (else ErrQuota) and the relay-wide total stays within
	// total (else ErrStorageFull). The checks and the insert are atomic.
	CreateBlob(ctx context.Context, b BlobRecord, quota, total int64) error
	GetBlob(ctx context.Context, id string) (BlobRecord, error)
	// PutChunk stores or replaces chunk n; ErrTooLarge if the blob's stored bytes would exceed maxTotal.
	PutChunk(ctx context.Context, id string, n uint32, data []byte, maxTotal int64) error
	GetChunk(ctx context.Context, id string, n uint32) ([]byte, error)
	DeleteBlob(ctx context.Context, id string) error
}

// Purger drops expired state so it does not accumulate.
type Purger interface {
	// PurgeExpired deletes everything expired at now: invites, rooms, blobs with their
	// chunk bytes, and queued frames past their TTL. It never resets a queue's seq.
	PurgeExpired(ctx context.Context, now time.Time) error
}

// Backend is everything a relay persists. A new storage engine implements it and
// plugs into New without changes to the server.
type Backend interface {
	Registry
	AllowLists
	Queues
	RoomStore
	Blobs
	Purger
}
