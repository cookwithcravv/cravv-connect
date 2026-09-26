package store

import (
	"context"
	"errors"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// LinkDirection says which side asked for the link.
type LinkDirection string

const (
	LinkInbound  LinkDirection = "in"  // the peer requested it
	LinkOutbound LinkDirection = "out" // we requested it
)

// LinkState is a link's lifecycle state (v2 spec 3.4). A pending inbound
// link is a request waiting for this side's decision; a pending outbound
// link waits for the peer's.
type LinkState string

const (
	LinkPending LinkState = "pending"
	LinkActive  LinkState = "active"
	LinkClosed  LinkState = "closed"
)

// Link connects a local shared session to a session on a peer machine. It
// is keyed by (Peer, ID); Num is the local handle shown to humans and agents.
type Link struct {
	Num           int64 // assigned by InsertLink, never reused
	Peer          core.MachineID
	ID            string // link_id, minted by the requester
	Direction     LinkDirection
	Session       string // local shared session ID
	RemoteSession string // the peer's session ID
	RemoteName    string // the peer's session name (peer-chosen, validated)
	RemotePurpose string
	PermissionIn  core.Permission // what the peer may do to our session
	PermissionOut core.Permission // what the peer lets us do ("" until known)
	Proposed      core.Permission // pending: the level the requester proposed
	Note          string          // pending inbound: the requester's note
	State         LinkState
	RemoteAway    bool   // the peer reported its session away (link.state)
	Reason        string // closed: the link.closed or link.rejected reason
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ExpiresAt     time.Time // pending: when the request times out
}

// Open reports whether the link is pending or active.
func (l Link) Open() bool { return l.State == LinkPending || l.State == LinkActive }

// LinkFilter selects links; zero fields match everything.
type LinkFilter struct {
	Peer          core.MachineID
	Session       string
	States        []LinkState
	Direction     LinkDirection
	ExpiredBefore time.Time // ExpiresAt != zero AND ExpiresAt <= this
}

// ErrLinkExists is returned by InsertLink when (Peer, ID) is already stored.
var ErrLinkExists = errors.New("link already exists")

// LinkStore persists links.
type LinkStore interface {
	InsertLink(ctx context.Context, l Link) (Link, error)
	GetLink(ctx context.Context, peer core.MachineID, id string) (Link, error) // core.ErrNotFound
	GetLinkByNum(ctx context.Context, num int64) (Link, error)                 // core.ErrNotFound
	ListLinks(ctx context.Context, f LinkFilter) ([]Link, error)               // by Num
	// UpdateLink reads, mutates and writes the link atomically.
	// mutate runs inside the store's transaction and MUST NOT call the store.
	UpdateLink(ctx context.Context, peer core.MachineID, id string, mutate func(*Link) error) (Link, error)
	// PurgeClosedLinks deletes closed links whose UpdatedAt < t.
	PurgeClosedLinks(ctx context.Context, t time.Time) (int, error)
}
