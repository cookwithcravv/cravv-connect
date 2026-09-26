package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Audit event types for offer rules.
const (
	EvOfferSet    = "offer_set"
	EvOfferRemove = "offer_remove"
)

// Errors for invalid offer rules (the API maps them to bad_request).
var (
	ErrBadOffer          = errors.New("invalid offer")
	ErrBadFolder         = errors.New("folder not allowed")
	ErrShellNotConfirmed = errors.New(`run mode shell lets the peer run commands as your user on this machine: type "shell" to confirm`)
)

// OfferInput is an offer rule as the owner sets it. Zero numbers and
// durations take the spec 6.1 defaults; an empty agent is claude and an
// empty run mode is read-only.
type OfferInput struct {
	Peer           string // alias or machine ID
	Label          string
	Folder         string
	Agent          string
	Permission     core.Permission
	RunMode        core.RunMode
	ShellConfirm   string // must be "shell" when RunMode is shell
	MaxConcurrent  int
	IdleTimeout    time.Duration
	MaxTurnsPerRun int
	RunTimeout     time.Duration
	RunsPerHour    int
	RunsPerDay     int
}

// FolderRules checks offer folders (v2 spec 6.1): absolute, an existing
// folder, not the home folder itself, and neither containing nor inside
// the cravv-connect state folder.
type FolderRules struct {
	Home     string // the user's home folder
	StateDir string // ~/.cravv-connect (config.Paths.Home)
}

// resolve returns p with symlinks resolved, or p cleaned when it cannot be.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Check validates folder and returns it with symlinks resolved.
func (r FolderRules) Check(folder string) (string, error) {
	if !filepath.IsAbs(folder) {
		return "", fmt.Errorf("%w: %q is not an absolute path", ErrBadFolder, folder)
	}
	clean := filepath.Clean(folder)
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("%w: %s does not exist", ErrBadFolder, clean)
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: %s is not a folder", ErrBadFolder, clean)
	}
	if r.Home != "" && (clean == filepath.Clean(r.Home) || real == resolve(r.Home)) {
		return "", fmt.Errorf("%w: your home folder itself cannot be offered; choose a project folder", ErrBadFolder)
	}
	if r.StateDir != "" {
		state := resolve(r.StateDir)
		for _, p := range []string{clean, real} {
			if within(state, p) || within(p, state) || within(filepath.Clean(r.StateDir), p) {
				return "", fmt.Errorf("%w: %s holds the cravv-connect state folder", ErrBadFolder, clean)
			}
		}
	}
	return real, nil
}

// Recheck runs before every managed run: the folder must still pass Check
// and resolve to the place it did when the rule was set (a symlink that
// moved fails).
func (r FolderRules) Recheck(o store.Offer) error {
	real, err := r.Check(o.Folder)
	if err != nil {
		return err
	}
	if real != o.RealFolder {
		return fmt.Errorf("%w: %s now resolves to %s, not %s; set the offer again", ErrBadFolder, o.Folder, real, o.RealFolder)
	}
	return nil
}

// OfferObserver is told when an offer is removed (its managed sessions close).
type OfferObserver interface {
	OfferRemoved(ctx context.Context, o store.Offer)
}

// OfferService owns the managed-session offer rules (v2 spec 6.1). Setting
// and removing a rule needs the password (AuthPassword).
type OfferService struct {
	store   store.OfferStore
	peers   PeerResolver
	folders FolderRules
	clock   core.Clock
	audit   audit.Logger

	mu        sync.Mutex
	observers []OfferObserver
}

// NewOfferService builds the service.
func NewOfferService(st store.OfferStore, peers PeerResolver, folders FolderRules, clock core.Clock, lg audit.Logger) *OfferService {
	if lg == nil {
		lg = audit.Nop{}
	}
	return &OfferService{store: st, peers: peers, folders: folders, clock: clock, audit: lg}
}

// AddObserver registers o for removed offers.
func (s *OfferService) AddObserver(o OfferObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, o)
}

// Folders returns the folder rules (the SessionHost re-checks with them).
func (s *OfferService) Folders() FolderRules { return s.folders }

func between[T int | time.Duration](name string, v, lo, hi T) error {
	if v < lo || v > hi {
		return fmt.Errorf("%w: %s must be between %v and %v", ErrBadOffer, name, lo, hi)
	}
	return nil
}

func orDefault[T int | time.Duration](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}

// build validates in and returns the offer it describes for peer.
func (s *OfferService) build(in OfferInput, peer store.Peer) (store.Offer, error) {
	o := store.Offer{
		Peer: peer.MachineID, Label: strings.TrimSpace(in.Label), Agent: strings.TrimSpace(in.Agent),
		Permission: in.Permission, RunMode: in.RunMode,
		MaxConcurrent: orDefault(in.MaxConcurrent, core.DefaultMaxConcurrent), IdleTimeout: orDefault(in.IdleTimeout, core.DefaultIdleTimeout),
		MaxTurnsPerRun: orDefault(in.MaxTurnsPerRun, core.DefaultMaxTurnsPerRun), RunTimeout: orDefault(in.RunTimeout, core.DefaultRunTimeout),
		RunsPerHour: orDefault(in.RunsPerHour, core.DefaultRunsPerHour), RunsPerDay: orDefault(in.RunsPerDay, core.DefaultRunsPerDay),
	}
	if !core.ValidOfferLabel(o.Label) {
		return o, fmt.Errorf("%w: label must be 1 to %d characters of a-z, 0-9 and -, not starting with -", ErrBadOffer, core.MaxOfferLabel)
	}
	if o.Agent == "" {
		o.Agent = core.ManagedAgentClaude
	}
	if o.Agent != core.ManagedAgentClaude {
		return o, fmt.Errorf("%w: agent %q is not supported (use claude)", ErrBadOffer, o.Agent)
	}
	if o.Permission != core.PermMessages && o.Permission != core.PermTasksAuto {
		return o, fmt.Errorf("%w: permission must be messages or tasks-auto (a managed session has no human to ask)", ErrBadOffer)
	}
	if o.RunMode == "" {
		o.RunMode = core.RunReadOnly
	}
	if !o.RunMode.Valid() {
		return o, fmt.Errorf("%w: run mode must be read-only, edit-in-folder or shell", ErrBadOffer)
	}
	if o.RunMode == core.RunShell && strings.TrimSpace(in.ShellConfirm) != "shell" {
		return o, ErrShellNotConfirmed
	}
	for _, err := range []error{
		between("max_concurrent", o.MaxConcurrent, 1, 20),
		between("idle_timeout", o.IdleTimeout, time.Minute, 7*24*time.Hour),
		between("max_turns_per_run", o.MaxTurnsPerRun, 1, 1000),
		between("run_timeout", o.RunTimeout, time.Second, 24*time.Hour),
		between("runs_per_hour", o.RunsPerHour, 1, 1000),
		between("runs_per_day", o.RunsPerDay, 1, 10000),
	} {
		if err != nil {
			return o, err
		}
	}
	real, err := s.folders.Check(in.Folder)
	if err != nil {
		return o, err
	}
	o.Folder, o.RealFolder = filepath.Clean(in.Folder), real
	return o, nil
}

// Set creates or replaces the offer with in.Label for in.Peer. It needs AuthPassword.
func (s *OfferService) Set(ctx context.Context, in OfferInput, auth Authority) (store.Offer, error) {
	if auth < AuthPassword {
		return store.Offer{}, fmt.Errorf("editing offer rules: %w", core.ErrAuthRequired)
	}
	peer, _, err := s.peers.Resolve(ctx, in.Peer)
	if err != nil {
		return store.Offer{}, err
	}
	o, err := s.build(in, peer)
	if err != nil {
		return o, err
	}
	now := s.clock.Now()
	o.ID, o.CreatedAt, o.UpdatedAt = core.NewIDAt(s.clock), now, now
	if cur, err := s.find(ctx, peer.MachineID, o.Label); err == nil {
		o.ID, o.CreatedAt = cur.ID, cur.CreatedAt
	} else if !errors.Is(err, core.ErrNotFound) {
		return o, err
	}
	if err := s.store.PutOffer(ctx, o); err != nil {
		return o, err
	}
	_ = s.audit.Record(audit.Event{Type: EvOfferSet, Peer: peer.MachineID, Alias: peer.Alias, ItemID: o.ID, Detail: map[string]any{
		"label": o.Label, "folder": o.Folder, "permission": string(o.Permission), "run_mode": string(o.RunMode),
	}})
	return o, nil
}

// find returns the peer's offer with label.
func (s *OfferService) find(ctx context.Context, peer core.MachineID, label string) (store.Offer, error) {
	list, err := s.store.ListOffers(ctx, peer)
	if err != nil {
		return store.Offer{}, err
	}
	for _, o := range list {
		if o.Label == label {
			return o, nil
		}
	}
	return store.Offer{}, fmt.Errorf("offer %q: %w", label, core.ErrNotFound)
}

// Remove deletes the peer's offer with label; its managed sessions close.
// It needs AuthPassword.
func (s *OfferService) Remove(ctx context.Context, peerName, label string, auth Authority) (store.Offer, error) {
	if auth < AuthPassword {
		return store.Offer{}, fmt.Errorf("editing offer rules: %w", core.ErrAuthRequired)
	}
	peer, _, err := s.peers.Resolve(ctx, peerName)
	if err != nil {
		return store.Offer{}, err
	}
	o, err := s.find(ctx, peer.MachineID, strings.TrimSpace(label))
	if err != nil {
		return o, err
	}
	if err := s.remove(ctx, o); err != nil {
		return o, err
	}
	_ = s.audit.Record(audit.Event{Type: EvOfferRemove, Peer: peer.MachineID, Alias: peer.Alias, ItemID: o.ID, Detail: map[string]any{"label": o.Label}})
	return o, nil
}

func (s *OfferService) remove(ctx context.Context, o store.Offer) error {
	if err := s.store.DeleteOffer(ctx, o.ID); err != nil {
		return err
	}
	s.mu.Lock()
	obs := append([]OfferObserver(nil), s.observers...)
	s.mu.Unlock()
	for _, ob := range obs {
		ob.OfferRemoved(ctx, o)
	}
	return nil
}

// List returns the offers to peer (alias or machine ID), or all when peer is "".
func (s *OfferService) List(ctx context.Context, peer string) ([]store.Offer, error) {
	if strings.TrimSpace(peer) == "" {
		return s.store.ListOffers(ctx, "")
	}
	p, _, err := s.peers.Resolve(ctx, peer)
	if err != nil {
		return nil, err
	}
	return s.store.ListOffers(ctx, p.MachineID)
}

// ForPeer returns the offers made to a paired machine (discovery).
func (s *OfferService) ForPeer(ctx context.Context, peer core.MachineID) ([]store.Offer, error) {
	return s.store.ListOffers(ctx, peer)
}

// Get returns an offer.
func (s *OfferService) Get(ctx context.Context, id string) (store.Offer, error) {
	return s.store.GetOffer(ctx, id)
}

// PeerCutOff implements PeerCutOffObserver: unpairing a machine (either
// side) removes the offers made to it. Pausing keeps them.
func (s *OfferService) PeerCutOff(ctx context.Context, peer store.Peer, reason string) error {
	if reason != CutOffUnpaired {
		return nil
	}
	list, err := s.store.ListOffers(ctx, peer.MachineID)
	if err != nil {
		return err
	}
	var errs []error
	for _, o := range list {
		errs = append(errs, s.remove(ctx, o))
	}
	return errors.Join(errs...)
}
