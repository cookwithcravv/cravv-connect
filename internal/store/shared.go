package store

import (
	"context"
	"errors"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// SharedSession is a session an agent chat shared (v2 spec 3.2). It is
// separate from SessionRecord, which tracks IPC attachments.
type SharedSession struct {
	ID           string
	Name         string // unique among open and away sessions
	Purpose      string
	Kind         core.SessionKind
	Agent        string
	ProjectDir   string // local only; never sent to peers
	Visibility   core.Visibility
	State        core.SessionState
	ReattachHash string // hex SHA-256 of the reattach token
	WakeHash     string // hex SHA-256 of the wake token
	Cursor       int64  // inbox read position
	CreatedAt    time.Time
	StateSince   time.Time // when State last changed (the away grace runs from here)
}

// ErrNameTaken is returned when an open or away session already has the name.
var ErrNameTaken = errors.New("a shared session with this name is already open")

// SharedSessionStore persists shared sessions. Method names are distinct from
// SessionStore's so one type can implement both.
type SharedSessionStore interface {
	// PutShared upserts by ID. It fails with ErrNameTaken when another open or
	// away session has the same name.
	PutShared(ctx context.Context, s SharedSession) error
	GetShared(ctx context.Context, id string) (SharedSession, error) // core.ErrNotFound
	// SharedByWakeHash and SharedByReattachHash find a session that is not
	// closed by a token hash (core.ErrNotFound otherwise).
	SharedByWakeHash(ctx context.Context, hash string) (SharedSession, error)
	SharedByReattachHash(ctx context.Context, hash string) (SharedSession, error)
	// ListShared returns sessions in any of states (all when none), oldest first.
	ListShared(ctx context.Context, states ...core.SessionState) ([]SharedSession, error)
	SetSharedCursor(ctx context.Context, id string, cursor int64) error
	// PurgeClosedShared deletes closed sessions whose StateSince < t.
	PurgeClosedShared(ctx context.Context, t time.Time) (int, error)
}
