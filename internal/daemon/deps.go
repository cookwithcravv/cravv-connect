package daemon

import (
	"context"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/sealing"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// Narrow interfaces so each service depends only on what it uses (ISP).

// MailboxProvider returns the current live mailbox, or false when offline or killed.
type MailboxProvider interface {
	Mailbox() (transport.Mailbox, bool)
}

// EnvelopeSender enqueues an envelope for reliable delivery. linkID is
// required for link-scoped kinds and "" otherwise. Implemented by *Outbound.
type EnvelopeSender interface {
	SendEnvelope(ctx context.Context, to core.MachineID, kind core.Kind, linkID string, body any) (string, error)
}

// OutboxControl is what PeerService needs from the outbound pipeline. Implemented by *Outbound.
type OutboxControl interface {
	EnvelopeSender
	// SendDirect seals and sends one envelope right now, bypassing the outbox (best effort).
	SendDirect(ctx context.Context, peer store.Peer, kind core.Kind, body any) error
	Hold(ctx context.Context, peer core.MachineID) error
	Release(ctx context.Context, peer core.MachineID) error
	Forget(ctx context.Context, peer core.MachineID) error
}

// PrekeySource resolves our private prekeys and reports the current signed one. Implemented by *PrekeyManager.
type PrekeySource interface {
	sealing.PrekeyResolver
	Current(ctx context.Context) (keys.SignedPrekey, error)
}

// PrekeyProvider guarantees a current prekey exists. Implemented by *PrekeyManager.
type PrekeyProvider interface {
	EnsureCurrent(ctx context.Context) (keys.SignedPrekey, error)
}

// PeerCutOffObserver is told when we pause or unpair a peer (or it unpairs
// us), before its outbox is held or deleted: TaskService rejects its tasks
// awaiting approval and FileService declines its held files.
type PeerCutOffObserver interface {
	PeerCutOff(ctx context.Context, peer store.Peer, reason string) error
}

// Resealer re-queues an outbox item for a fresh seal. Implemented by *Outbound.
type Resealer interface {
	Reseal(ctx context.Context, to core.MachineID, msgID string) error
}

// DeliveryMarker drops outbox items the peer confirmed. Implemented by *Outbound.
type DeliveryMarker interface {
	MarkDelivered(ctx context.Context, from core.MachineID, ids []string) error
}

// OutboxReleaser moves held items for a peer back to pending. Implemented by *Outbound.
type OutboxReleaser interface {
	Release(ctx context.Context, peer core.MachineID) error
}

// PeerStateUpdater applies peer-initiated state changes. Implemented by *PeerService.
type PeerStateUpdater interface {
	MarkPausedByPeer(ctx context.Context, id core.MachineID, paused bool) error
	RemoveByPeer(ctx context.Context, id core.MachineID) error
}

// Registrar registers this machine's mailbox with a relay invite. Implemented by *Daemon (Task 20).
type Registrar interface {
	Registered() bool
	EnsureRegistered(ctx context.Context, invite string) error
}

// Notifier wakes waiting sessions. Implemented by *InboxService.
type Notifier interface {
	Notify()
}

// DesktopNotifier shows a desktop notification (darwin: osascript; elsewhere a no-op).
type DesktopNotifier interface {
	Notify(title, text string)
}
