package store

import (
	"context"
	"errors"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Offer is a managed-session rule the owner made for one paired machine
// (v2 spec 6.1). The peer sees only ID, Label, Agent and Permission.
type Offer struct {
	ID             string         // core ID; the peer names it in link.request
	Peer           core.MachineID // the paired machine the offer is made to
	Label          string
	Folder         string // absolute path as the owner gave it (cleaned)
	RealFolder     string // Folder with symlinks resolved when the rule was set
	Agent          string
	Permission     core.Permission // messages or tasks-auto
	RunMode        core.RunMode
	MaxConcurrent  int
	IdleTimeout    time.Duration
	MaxTurnsPerRun int
	RunTimeout     time.Duration
	RunsPerHour    int // per link
	RunsPerDay     int // per peer machine
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ManagedSession is what the daemon keeps about a managed session beside
// its SharedSession record (same ID).
type ManagedSession struct {
	SessionID    string
	OfferID      string
	Peer         core.MachineID
	LinkID       string // the session's one link
	AgentSession string // the agent CLI's session ID (a UUID the daemon chose)
	Started      bool   // the agent has a conversation to resume
	LastActive   time.Time
	CreatedAt    time.Time
}

// ManagedRun records one run start, for the per-link and per-machine caps.
type ManagedRun struct {
	ID        string
	SessionID string
	Peer      core.MachineID
	LinkID    string
	StartedAt time.Time
}

// RunFilter selects runs; zero fields match everything.
type RunFilter struct {
	Peer   core.MachineID
	LinkID string
	Since  time.Time // StartedAt >= Since
}

// RunCaps are the limits AddRunCapped enforces: at most PerLink runs on
// the run's link since LinkSince, and at most PerPeer runs for its machine
// since PeerSince.
type RunCaps struct {
	PerLink   int
	LinkSince time.Time
	PerPeer   int
	PeerSince time.Time
}

// RunCap names the cap that refused a run.
type RunCap int

const (
	RunCapNone RunCap = iota // the run was recorded
	RunCapLink               // the per-link cap
	RunCapPeer               // the per-machine cap
)

// ErrOfferLabelTaken is returned when the peer already has an offer with the label.
var ErrOfferLabelTaken = errors.New("this machine already has an offer with that label")

// OfferStore persists offers, managed sessions and run records.
type OfferStore interface {
	// PutOffer upserts by ID (ErrOfferLabelTaken on a (Peer, Label) clash).
	PutOffer(ctx context.Context, o Offer) error
	GetOffer(ctx context.Context, id string) (Offer, error) // core.ErrNotFound
	// ListOffers returns the peer's offers ("" for all), by label.
	ListOffers(ctx context.Context, peer core.MachineID) ([]Offer, error)
	DeleteOffer(ctx context.Context, id string) error // core.ErrNotFound

	// PutManaged upserts by SessionID. The record goes when its shared
	// session is purged.
	PutManaged(ctx context.Context, m ManagedSession) error
	GetManaged(ctx context.Context, sessionID string) (ManagedSession, error) // core.ErrNotFound
	ListManaged(ctx context.Context) ([]ManagedSession, error)                // oldest first

	AddRun(ctx context.Context, r ManagedRun) error
	// AddRunCapped records r only if neither cap is reached, checking and
	// recording in one transaction; it returns the cap that refused it.
	AddRunCapped(ctx context.Context, r ManagedRun, caps RunCaps) (RunCap, error)
	CountRuns(ctx context.Context, f RunFilter) (int, error)
	PurgeRunsBefore(ctx context.Context, t time.Time) (int, error)
}
