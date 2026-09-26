package daemon

import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// DaemonStatus is what `status` reports. The API layer maps it to ipc.StatusResult.
type DaemonStatus struct {
	MachineID        core.MachineID
	DeviceName       string
	RelayURL         string
	RelayConnected   bool
	Killed           bool
	Peers            []store.Peer
	Online           map[core.MachineID]bool // relay connected and the peer was heard from within OnlineWindow
	Sessions         []string                // shared sessions that are open or away, as name (state)
	OutboxPending    int
	OutboxHeld       int
	InboxUnread      int // summed over open and away shared sessions
	PendingApprovals int
	Errors           []string
	LastSeen         map[core.MachineID]time.Time // when each peer was last heard from since the daemon started
}

// OutboxCounter counts undelivered outbox items. Implemented by store.OutboxStore.
type OutboxCounter interface {
	CountOutbox(ctx context.Context) (pending int, held int, err error)
}

// StatusDeps are the StatusService collaborators.
type StatusDeps struct {
	MachineID  core.MachineID
	DeviceName string
	RelayURL   string
	Mailboxes  MailboxProvider
	Killed     func() bool
	Peers      store.PeerStore
	Outbox     OutboxCounter
	Shared     *SessionService
	Inbox      *InboxService
	Tasks      *TaskService
	Activity   *PeerActivity
	Errors     []func() []string // each source reports current problems
}

// StatusService assembles DaemonStatus. It works while the kill switch is on.
type StatusService struct{ d StatusDeps }

// NewStatusService builds a StatusService.
func NewStatusService(d StatusDeps) *StatusService { return &StatusService{d: d} }

// Status reports the machine's current state.
func (s *StatusService) Status(ctx context.Context) (DaemonStatus, error) {
	_, connected := s.d.Mailboxes.Mailbox()
	st := DaemonStatus{
		MachineID: s.d.MachineID, DeviceName: s.d.DeviceName, RelayURL: s.d.RelayURL,
		RelayConnected: connected, Killed: s.d.Killed(), Online: map[core.MachineID]bool{},
		LastSeen: map[core.MachineID]time.Time{},
	}
	peers, err := s.d.Peers.ListPeers(ctx)
	if err != nil {
		return st, err
	}
	st.Peers = peers
	for _, p := range peers {
		st.Online[p.MachineID] = connected && !p.Paused && !p.PausedByPeer && s.d.Activity.Online(p.MachineID)
		if at, ok := s.d.Activity.LastSeen(p.MachineID); ok {
			st.LastSeen[p.MachineID] = at
		}
	}
	sessions, err := s.d.Shared.List(ctx, core.SessionOpen, core.SessionAway)
	if err != nil {
		return st, err
	}
	for _, rec := range sessions {
		st.Sessions = append(st.Sessions, rec.Name+" ("+string(rec.State)+")")
		byAlias, err := s.d.Inbox.Unread(ctx, rec.ID)
		if err != nil {
			return st, err
		}
		for _, n := range byAlias {
			st.InboxUnread += n
		}
	}
	if st.OutboxPending, st.OutboxHeld, err = s.d.Outbox.CountOutbox(ctx); err != nil {
		return st, err
	}
	if st.PendingApprovals, err = s.d.Tasks.PendingApprovals(ctx); err != nil {
		return st, err
	}
	for _, src := range s.d.Errors {
		st.Errors = append(st.Errors, src()...)
	}
	return st, nil
}
