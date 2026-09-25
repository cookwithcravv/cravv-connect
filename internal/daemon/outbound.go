package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

const (
	outboundBatch   = 100             // items per pass
	outboundTick    = time.Second     // pass interval when nothing wakes the loop
	maxRecentErrors = 20              // size of the Errors() ring
	errOffline      = "relay offline" // SendDirect error text
)

// ErrOffline is returned by SendDirect when there is no live mailbox.
var ErrOffline = errors.New(errOffline)

// Outbound enqueues envelopes in the persistent outbox and delivers them: seal to the
// peer's current prekey on every attempt, send, and react to the relay's status.
type Outbound struct {
	identity  *keys.Identity
	peers     store.PeerStore
	outbox    store.OutboxStore
	mailboxes MailboxProvider
	clock     core.Clock
	killed    func() bool
	logger    *slog.Logger
	wake      chan struct{}

	mu     sync.Mutex
	recent []string
}

// NewOutbound wires an Outbound. killed may be nil (never killed); logger may be nil.
func NewOutbound(id *keys.Identity, peers store.PeerStore, outbox store.OutboxStore, mailboxes MailboxProvider,
	clock core.Clock, killed func() bool, logger *slog.Logger) *Outbound {
	if killed == nil {
		killed = func() bool { return false }
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Outbound{
		identity: id, peers: peers, outbox: outbox, mailboxes: mailboxes,
		clock: clock, killed: killed, logger: logger,
		wake: make(chan struct{}, 1),
	}
}

// SendEnvelope builds an envelope and stores it in the outbox. It fails with an error
// wrapping core.ErrTooLarge, before enqueueing, when the sealed frame could not fit the
// relay frame limit. It fails with core.ErrPaused
// when we paused the peer (control kinds excepted). When the peer paused us the item is
// stored as held and goes out after control.resumed.
func (o *Outbound) SendEnvelope(ctx context.Context, to core.MachineID, kind core.Kind, fromSession, toSession string, body any) (string, error) {
	if o.killed() {
		return "", core.ErrKilled
	}
	peer, err := o.peers.GetPeer(ctx, to)
	if err != nil {
		return "", err
	}
	if peer.Paused && !kind.IsControl() {
		return "", core.ErrPaused
	}
	env, err := core.NewEnvelope(o.clock, o.identity.MachineID(), to, kind, body)
	if err != nil {
		return "", err
	}
	env.FromSession, env.ToSession = fromSession, toSession
	if err := sealing.FitsFrame(env); err != nil {
		return "", fmt.Errorf("message to %s: %w", peer.Alias, err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	status := store.OutboxPending
	if peer.PausedByPeer {
		status = store.OutboxHeld
	}
	now := o.clock.Now()
	if err := o.outbox.Enqueue(ctx, store.OutboxItem{
		ID: env.ID, To: to, Envelope: raw, Status: status, NextAttempt: now, CreatedAt: now,
	}); err != nil {
		return "", err
	}
	o.Wake()
	return env.ID, nil
}

// SendDirect seals and sends one envelope now, without the outbox. It is best effort and
// used for notices whose peer record is about to change (control.paused, control.unpaired).
func (o *Outbound) SendDirect(ctx context.Context, peer store.Peer, kind core.Kind, body any) error {
	if o.killed() {
		return core.ErrKilled
	}
	mb, ok := o.mailboxes.Mailbox()
	if !ok {
		return ErrOffline
	}
	env, err := core.NewEnvelope(o.clock, o.identity.MachineID(), peer.MachineID, kind, body)
	if err != nil {
		return err
	}
	frame, err := o.seal(peer, env)
	if err != nil {
		return err
	}
	st, err := mb.Send(ctx, peer.MachineID, env.ID, frame)
	if err != nil {
		return err
	}
	if st != transport.SendQueued {
		return fmt.Errorf("relay refused %s: %s", kind, st)
	}
	return nil
}

// Wake asks the send loop for a pass now.
func (o *Outbound) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Run is the send loop: a pass on every wake and every second, until ctx ends.
func (o *Outbound) Run(ctx context.Context) error {
	ticker := time.NewTicker(outboundTick)
	defer ticker.Stop()
	for {
		if err := o.SendDue(ctx); err != nil && ctx.Err() == nil {
			o.logger.Warn("outbox pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-o.wake:
		case <-ticker.C:
		}
	}
}

// SendDue makes one pass over due outbox items. It does nothing while killed or offline.
func (o *Outbound) SendDue(ctx context.Context) error {
	if o.killed() {
		return nil
	}
	mb, ok := o.mailboxes.Mailbox()
	if !ok {
		return nil
	}
	items, err := o.outbox.Due(ctx, o.clock.Now(), outboundBatch)
	if err != nil {
		return err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := o.attempt(ctx, mb, it); err != nil {
			return err
		}
	}
	return nil
}

// attempt tries one item. It returns only store errors; delivery problems update the item.
func (o *Outbound) attempt(ctx context.Context, mb transport.Mailbox, it store.OutboxItem) error {
	peer, err := o.peers.GetPeer(ctx, it.To)
	if errors.Is(err, core.ErrNotFound) {
		o.recordError(fmt.Sprintf("dropped message %s: peer no longer paired", it.ID))
		return o.outbox.Delete(ctx, it.ID)
	}
	if err != nil {
		return err
	}
	var env core.Envelope
	if err := json.Unmarshal(it.Envelope, &env); err != nil {
		o.recordError(fmt.Sprintf("dropped message %s: corrupt outbox entry", it.ID))
		return o.outbox.Delete(ctx, it.ID)
	}
	if (peer.Paused && !env.Kind.IsControl()) || peer.PausedByPeer {
		return o.outbox.SetStatus(ctx, it.ID, store.OutboxHeld, it.Attempts, it.NextAttempt)
	}
	frame, err := o.seal(peer, env)
	if errors.Is(err, core.ErrTooLarge) {
		o.recordError(fmt.Sprintf("dropped message %s to %s: too large to seal", it.ID, peer.Alias))
		return o.outbox.Delete(ctx, it.ID)
	}
	if err != nil {
		o.recordError(fmt.Sprintf("cannot seal to %s: %v", peer.Alias, err))
		return o.backoff(ctx, it)
	}
	st, err := mb.Send(ctx, it.To, it.ID, frame)
	if err != nil {
		o.logger.Info("relay send failed", "id", it.ID, "err", err)
		return o.backoff(ctx, it)
	}
	switch st {
	case transport.SendQueued:
		return o.outbox.SetStatus(ctx, it.ID, store.OutboxQueued, it.Attempts+1, o.clock.Now())
	case transport.SendNotAllowed:
		return o.markPausedByPeer(ctx, peer)
	case transport.SendTooLarge:
		o.recordError(fmt.Sprintf("dropped message %s to %s: too large for the relay", it.ID, peer.Alias))
		return o.outbox.Delete(ctx, it.ID)
	case transport.SendUnknownMailbox:
		o.recordError(fmt.Sprintf("dropped message %s: %s has no mailbox on this relay", it.ID, peer.Alias))
		return o.outbox.Delete(ctx, it.ID)
	case transport.SendQueueFull:
		o.recordError(fmt.Sprintf("peer's mailbox full: %s", peer.Alias))
		return o.backoff(ctx, it)
	default: // rate_limited and anything unknown
		return o.backoff(ctx, it)
	}
}

func (o *Outbound) seal(peer store.Peer, env core.Envelope) ([]byte, error) {
	spk := keys.SignedPrekeyFromWire(peer.Prekey)
	if err := spk.Verify(peer.IK); err != nil {
		return nil, fmt.Errorf("no valid prekey: %w", err)
	}
	frame, err := sealing.Seal(o.identity, spk, env)
	if err != nil {
		return nil, err
	}
	return frame.Marshal()
}

func (o *Outbound) markPausedByPeer(ctx context.Context, peer store.Peer) error {
	if err := o.outbox.HoldPeer(ctx, peer.MachineID); err != nil {
		return err
	}
	if peer.PausedByPeer {
		return nil
	}
	peer.PausedByPeer = true
	return o.peers.PutPeer(ctx, peer)
}

// backoffDelay is BackoffMin doubled per failed attempt, capped at BackoffMax.
func backoffDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := core.BackoffMin
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= core.BackoffMax {
			return core.BackoffMax
		}
	}
	return d
}

func (o *Outbound) backoff(ctx context.Context, it store.OutboxItem) error {
	attempts := it.Attempts + 1
	return o.outbox.SetStatus(ctx, it.ID, store.OutboxPending, attempts, o.clock.Now().Add(backoffDelay(attempts)))
}

// MarkDelivered deletes the items `from` confirmed. IDs of items addressed to another
// peer are ignored, so a peer cannot clear our outbox for someone else.
func (o *Outbound) MarkDelivered(ctx context.Context, from core.MachineID, ids []string) error {
	var mine []string
	for _, id := range ids {
		it, err := o.outbox.Get(ctx, id)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if it.To == from {
			mine = append(mine, id)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	return o.outbox.Delete(ctx, mine...)
}

// Reseal puts an item back to pending so the next pass seals it to the peer's current
// prekey (used after control.stale_prekey). The item must be addressed to `to`.
func (o *Outbound) Reseal(ctx context.Context, to core.MachineID, msgID string) error {
	it, err := o.outbox.Get(ctx, msgID)
	if err != nil {
		return err
	}
	if it.To != to {
		return fmt.Errorf("outbox item %s: %w", msgID, core.ErrNotFound)
	}
	if err := o.outbox.SetStatus(ctx, msgID, store.OutboxPending, it.Attempts, o.clock.Now()); err != nil {
		return err
	}
	o.Wake()
	return nil
}

// Hold stops sending to a peer: its pending and queued items become held.
func (o *Outbound) Hold(ctx context.Context, peer core.MachineID) error {
	return o.outbox.HoldPeer(ctx, peer)
}

// Release moves a peer's held items back to pending and wakes the loop.
func (o *Outbound) Release(ctx context.Context, peer core.MachineID) error {
	if err := o.outbox.ReleasePeer(ctx, peer, o.clock.Now()); err != nil {
		return err
	}
	o.Wake()
	return nil
}

// Forget deletes every outbox item for a peer (unpair).
func (o *Outbound) Forget(ctx context.Context, peer core.MachineID) error {
	return o.outbox.DeleteOutboxForPeer(ctx, peer)
}

// PurgeOld deletes items older than core.OutboxRetention.
func (o *Outbound) PurgeOld(ctx context.Context) (int, error) {
	return o.outbox.PurgeOutboxBefore(ctx, o.clock.Now().Add(-core.OutboxRetention))
}

// Errors returns recent delivery problems, oldest first, for status.
func (o *Outbound) Errors() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.recent...)
}

func (o *Outbound) recordError(msg string) {
	o.logger.Warn("outbound", "msg", msg)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.recent = append(o.recent, msg)
	if len(o.recent) > maxRecentErrors {
		o.recent = o.recent[len(o.recent)-maxRecentErrors:]
	}
}
