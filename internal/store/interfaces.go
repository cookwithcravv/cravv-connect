// Package store declares the repository interfaces and record types used by
// the daemon. Implementations live in subpackages (store/sqlite).
package store

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Peer is a paired machine as seen from this machine.
type Peer struct {
	MachineID    core.MachineID
	IK           ed25519.PublicKey
	Alias        string
	TrustIn      core.TrustLevel // what THEY may do on THIS machine
	Prekey       core.SignedPrekeyWire
	RelayURL     string
	Paused       bool // paused by me
	PausedByPeer bool
	PairedAt     time.Time
}

type PeerStore interface {
	PutPeer(ctx context.Context, p Peer) error                    // upsert by MachineID; returns store.ErrAliasTaken if another peer has the alias
	GetPeer(ctx context.Context, id core.MachineID) (Peer, error) // core.ErrNotFound
	GetPeerByAlias(ctx context.Context, alias string) (Peer, error)
	ListPeers(ctx context.Context) ([]Peer, error)
	DeletePeer(ctx context.Context, id core.MachineID) error
}

var ErrAliasTaken = errors.New("alias already in use")

type PrekeyRecord struct {
	ID           string
	Priv         []byte
	CreatedAt    time.Time
	SupersededAt *time.Time
}

type PrekeyStore interface {
	PutPrekey(ctx context.Context, r PrekeyRecord) error
	CurrentPrekey(ctx context.Context) (PrekeyRecord, error) // newest with SupersededAt==nil
	GetPrekey(ctx context.Context, id string) (PrekeyRecord, error)
	SupersedeAllExcept(ctx context.Context, id string, at time.Time) error
	DeleteSupersededBefore(ctx context.Context, t time.Time) (int, error)
}

type OutboxStatus string

const (
	OutboxPending OutboxStatus = "pending"
	OutboxQueued  OutboxStatus = "queued"
	OutboxHeld    OutboxStatus = "held"
)

type OutboxItem struct {
	ID          string // == Envelope.ID
	To          core.MachineID
	Envelope    []byte // JSON of core.Envelope (plaintext; re-sealed on every attempt)
	Status      OutboxStatus
	Attempts    int
	NextAttempt time.Time
	CreatedAt   time.Time
}

type OutboxStore interface {
	Enqueue(ctx context.Context, it OutboxItem) error
	Due(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error) // Status=pending AND NextAttempt<=now, oldest first
	Get(ctx context.Context, id string) (OutboxItem, error)
	SetStatus(ctx context.Context, id string, st OutboxStatus, attempts int, next time.Time) error
	Delete(ctx context.Context, ids ...string) error                         // on delivered
	HoldPeer(ctx context.Context, to core.MachineID) error                   // pending|queued -> held, except control.* envelopes
	ReleasePeer(ctx context.Context, to core.MachineID, now time.Time) error // held -> pending, next=now
	DeleteOutboxForPeer(ctx context.Context, to core.MachineID) error        // all items to a peer (unpair)
	PurgeOutboxBefore(ctx context.Context, t time.Time) (int, error)         // CreatedAt < t
	CountOutbox(ctx context.Context) (pending int, held int, err error)      // pending = status pending or queued
	// RequeueStale moves queued items whose NextAttempt <= now back to pending
	// (the relay dropped them after its TTL without a control.delivered).
	RequeueStale(ctx context.Context, now time.Time) (int, error)
}

type InboxItem struct {
	Seq         int64 // assigned by store (autoincrement)
	MsgID       string
	From        core.MachineID
	FromSession string
	ToSession   string // "" = machine-wide
	LinkID      string // the link the item arrived on ("" for v1 items)
	Kind        core.Kind
	Body        json.RawMessage
	TaskID      string
	Note        string // e.g. "(originally for claude@x)"
	ReceivedAt  time.Time
	ReadByAny   bool
}

type InboxStore interface {
	// AddItem stores it and returns its seq. A chat whose (MsgID, ToSession) is
	// already stored is not stored again; the existing seq is returned.
	AddItem(ctx context.Context, it InboxItem) (int64, error)
	// Visible to session: seq > after AND (ToSession=="" OR ToSession==session); ascending; limit
	ItemsFor(ctx context.Context, session string, after int64, limit int) ([]InboxItem, error)
	MarkRead(ctx context.Context, seqs []int64) error
	// cursor for a brand new session: max(seq) of items that are both older than `since` AND ReadByAny; 0 if none
	InitialCursor(ctx context.Context, since time.Time) (int64, error)
	// Re-inserts every item with ToSession=session as a new machine-wide item
	// (new seq, ReadByAny=false, Note=note) and deletes the originals, atomically.
	RedirectOrphans(ctx context.Context, session string, note string) (int, error)
	UnreadCount(ctx context.Context, session string, after int64) (int, map[core.MachineID]int, error)
	PurgeInboxBefore(ctx context.Context, t time.Time) (int, error) // ReceivedAt < t
	// HasInboxMsg reports whether any item carries msgID (handlers use it to
	// finish a delivery that failed after their own store write).
	HasInboxMsg(ctx context.Context, msgID string) (bool, error)
	// SessionItems returns items addressed to exactly this shared session
	// (ToSession == session) with seq > after, ascending, at most limit.
	SessionItems(ctx context.Context, session string, after int64, limit int) ([]InboxItem, error)
	// SessionUnread counts SessionItems(session, after), in total and per sender.
	SessionUnread(ctx context.Context, session string, after int64) (int, map[core.MachineID]int, error)
	// DeleteSessionItems deletes the session's items from one link with seq > after.
	DeleteSessionItems(ctx context.Context, session, linkID string, after int64) (int, error)
}

type SessionRecord struct {
	Name       string
	Agent      string
	ProjectDir string
	Cursor     int64
	LastSeen   time.Time
	Connected  bool
}

type SessionStore interface {
	PutSession(ctx context.Context, s SessionRecord) error
	GetSession(ctx context.Context, name string) (SessionRecord, error)
	ListSessions(ctx context.Context) ([]SessionRecord, error)
	SetCursor(ctx context.Context, name string, cursor int64) error
	DeleteSession(ctx context.Context, name string) error
}

type TaskDirection string

const (
	TaskInbound  TaskDirection = "in"
	TaskOutbound TaskDirection = "out"
)

type TaskNote struct {
	At    time.Time `json:"at"`
	Text  string    `json:"text"`
	MsgID string    `json:"msg_id,omitempty"` // the task.update that carried it (sender side)
}

type Task struct {
	ID           string
	Direction    TaskDirection
	Peer         core.MachineID
	FromSession  string // sender session (inbound) / local session (outbound)
	ToSession    string
	LinkID       string // the link the task travels on ("" for v1 tasks)
	Instructions string
	State        core.TaskState
	ClaimedBy    string
	Notes        []TaskNote
	Result       string
	ResultFiles  []core.FileRef
	Files        []core.FileRef
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExpiresAt    time.Time // zero = none
}

type TaskStore interface {
	PutTask(ctx context.Context, t Task) error
	GetTask(ctx context.Context, id string) (Task, error)
	// Atomic compare-and-set: fails with core.ErrBadTransition if current state not in from.
	// mutate runs inside the store's transaction and MUST NOT call the store
	// (any Store method, on this or another interface): doing so deadlocks.
	// It should only edit the *Task; do side effects after Transition returns.
	Transition(ctx context.Context, id string, from []core.TaskState, mutate func(*Task) error) (Task, error)
	ListTasks(ctx context.Context, f TaskFilter) ([]Task, error)
}

type TaskFilter struct {
	Direction     TaskDirection // "" = any
	States        []core.TaskState
	ClaimedBy     string
	Peer          core.MachineID
	LinkID        string
	ExpiredBefore time.Time // ExpiresAt != zero AND ExpiresAt <= this
}

type FileState string

const (
	FileOffered     FileState = "offered"
	FileHeld        FileState = "held"
	FileDownloading FileState = "downloading"
	FileDone        FileState = "done"
	FileFailed      FileState = "failed"
	FileDeclined    FileState = "declined"
	FileUploading   FileState = "uploading"
	FileSent        FileState = "sent"
)

type FileRecord struct {
	FileID    string
	Direction TaskDirection
	Peer      core.MachineID
	MsgID     string
	BlobID    string
	Name      string // sanitized for inbound
	Size      int64
	Chunks    uint32
	SHA256    []byte
	Key       []byte
	TaskID    string
	LinkID    string // the link the file travels on ("" for v1 files)
	Session   string // local shared session ID ("" for v1 files)
	State     FileState
	LocalPath string
	NextChunk uint32 // resume point
	Attempts  int
	Reason    string
	CreatedAt time.Time
}

type FileStore interface {
	PutFile(ctx context.Context, f FileRecord) error
	GetFile(ctx context.Context, id string) (FileRecord, error)
	ListFiles(ctx context.Context, states ...FileState) ([]FileRecord, error)
	// UpdateFile reads, mutates and writes the record atomically.
	// mutate runs inside the store's transaction and MUST NOT call the store
	// (any Store method, on this or another interface): doing so deadlocks.
	// It should only edit the *FileRecord; do side effects after UpdateFile returns.
	UpdateFile(ctx context.Context, id string, mutate func(*FileRecord) error) (FileRecord, error)
	// InboundBytes sums Size over the peer's inbound files that count toward its
	// quota: State=downloading, or State=done with CreatedAt >= since.
	InboundBytes(ctx context.Context, peer core.MachineID, since time.Time) (int64, error)
	// PurgeFilesBefore deletes finished records (done, failed, declined, sent)
	// with CreatedAt < t. Files on disk are not touched.
	PurgeFilesBefore(ctx context.Context, t time.Time) (int, error)
}

type DedupStore interface {
	SeenOrMark(ctx context.Context, id string, at time.Time) (seen bool, err error) // atomic
	Seen(ctx context.Context, id string) (bool, error)                              // read only: never marks
	PurgeDedupBefore(ctx context.Context, t time.Time) (int, error)                 // seen at < t
}

type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Settings keys
const (
	SettingKilled       = "killed"
	SettingAllowPaths   = "allow_paths" // JSON []string
	SettingIdentitySeed = "identity_seed_b64"
)

// Store is every repository in one handle; *sqlite.DB implements it.
// Method names are unique across the embedded interfaces so that one
// concrete type can implement all of them with distinct behavior.
type Store interface {
	PeerStore
	PrekeyStore
	OutboxStore
	InboxStore
	SessionStore
	SharedSessionStore
	LinkStore
	TaskStore
	FileStore
	DedupStore
	SettingsStore
	Close() error
}
