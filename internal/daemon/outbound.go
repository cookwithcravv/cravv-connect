package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/sealing"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

const (
	outboundBatch   = 100             // items per pass
	outboundTick    = time.Second     // pass interval when nothing wakes the loop
	maxRecentErrors = 20              // size of the Errors() ring
	errOffline      = "relay offline" // SendDirect error text
)

// PairingGrace is how long after pairing a relay not_allowed is taken to mean
// that the peer has not finished pairing (its human is still answering the
// prompts, so it has not allowed us on the relay yet) rather than a pause. In
// that window items are retried every PairingGraceRetry at most and the peer
// is not marked as pausing us; the peer's control.resumed, sent when it
// finalizes, clears any pause set before.
const (
	PairingGrace      = 30 * time.Minute
	PairingGraceRetry = 5 * time.Second
)

// NoMailboxRetryMax caps the backoff for an item the relay answered
// unknown_mailbox for. A peer that just paired or set up again may have no
// mailbox on this relay for a while, so such an item is retried (doubling
// from core.BackoffMin up to this) until it expires after
// core.OutboxRetention, and status says it is waiting.
const NoMailboxRetryMax = 30 * time.Minute

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
	stopped   func() bool // the kill switch is fully on: send nothing
	logger    *slog.Logger
	wake      chan struct{}

	pass    sync.Mutex // serializes SendDue passes (the loop and Flush)
	mu      sync.Mutex
	recent  []string
	waiting map[core.MachineID]*noMailbox // items the relay had no mailbox for, by peer
}

// noMailbox is a peer's items waiting for it to have a relay mailbox.
type noMailbox struct {
	alias string
	items map[string]time.Time // outbox item ID -> CreatedAt
}

// NewOutbound wires an Outbound. stopped reports that sending must stop (the kill
// switch is fully on); it must stay false during the kill flush, so the daemon passes
// the negation of KillSwitch.SendingAllowed, not Killed. It may be nil (never
// stopped); logger may be nil.
func NewOutbound(id *keys.Identity, peers store.PeerStore, outbox store.OutboxStore, mailboxes MailboxProvider,
	clock core.Clock, stopped func() bool, logger *slog.Logger) *Outbound {
	if stopped == nil {
		stopped = func() bool { return false }
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Outbound{
		identity: id, peers: peers, outbox: outbox, mailboxes: mailboxes,
		clock: clock, stopped: stopped, logger: logger,
		wake:    make(chan struct{}, 1),
		waiting: map[core.MachineID]*noMailbox{},
	}
}

// ErrNoLinkID is returned by SendEnvelope for a link-scoped kind (chat,
// task.*, file.offer) without a link ID: v2 peers drop such envelopes.
var ErrNoLinkID = errors.New("link-scoped message without a link")

// SendEnvelope builds an envelope and stores it in the outbox. While the kill switch is
// on it still enqueues (so task.update notices such as expired are not lost) but the
// send loop sends nothing until resume; refusing agent sends while killed is the IPC
// layer's job. It fails with an error
// wrapping core.ErrTooLarge, before enqueueing, when the sealed frame could not fit the
// relay frame limit. It fails with core.ErrPaused
// when we paused the peer (control kinds excepted). When the peer paused us the item is
// stored as held and goes out after control.resumed. Control kinds are never held: a
// held control.resumed would deadlock two peers that paused each other.
func (o *Outbound) SendEnvelope(ctx context.Context, to core.MachineID, kind core.Kind, linkID string, body any) (string, error) {
	if kind.Ephemeral() {
		return "", fmt.Errorf("%s is sent directly, never through the outbox", kind)
	}
	if kind.LinkScoped() && linkID == "" {
		return "", fmt.Errorf("%s needs a link: %w", kind, ErrNoLinkID)
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
	env.LinkID = linkID
	if err := sealing.FitsFrame(env); err != nil {
		return "", fmt.Errorf("message to %s: %w", peer.Alias, err)
	}
	raw, err := encodeNoHTML(env)
	if err != nil {
		return "", err
	}
	status := store.OutboxPending
	if peer.PausedByPeer && !kind.IsControl() {
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
	if o.stopped() {
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
	if st == transport.SendUnknownMailbox {
		return fmt.Errorf("%s has no mailbox on the relay yet (it may still be finishing pairing or setup)", peer.Alias)
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

// SendDue makes one pass over due outbox items, after moving queued items the relay has
// dropped (older than RelayTTL, never confirmed) back to pending. It does nothing while
// killed or offline.
func (o *Outbound) SendDue(ctx context.Context) error {
	if o.stopped() {
		return nil
	}
	o.pass.Lock()
	defer o.pass.Unlock()
	mb, ok := o.mailboxes.Mailbox()
	if !ok {
		return nil
	}
	now := o.clock.Now()
	if _, err := o.outbox.RequeueStale(ctx, now); err != nil {
		return err
	}
	items, err := o.outbox.Due(ctx, now, outboundBatch)
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
	if (peer.Paused || peer.PausedByPeer) && !env.Kind.IsControl() {
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
	// Mark the send in flight. The relay may deliver the frame, and the peer
	// may answer (control.stale_prekey, control.delivered), before the relay's
	// own reply reaches us; everything below is recorded only if the item is
	// still sending, so such a change is never overwritten. A daemon that
	// stops mid-send leaves the item sending until RequeueStale picks it up.
	claimed, err := o.outbox.SetStatusIf(ctx, it.ID, store.OutboxPending, store.OutboxSending, it.Attempts, o.clock.Now().Add(sendingTimeout))
	if err != nil || !claimed {
		return err // changed since Due (held, resealed, deleted): leave it
	}
	st, err := mb.Send(ctx, it.To, it.ID, frame)
	if err != nil {
		o.logger.Info("relay send failed", "id", it.ID, "err", err)
		return o.backoffSending(ctx, it)
	}
	if st == transport.SendUnknownMailbox {
		return o.waitForMailbox(ctx, peer, it)
	}
	// Any other answer means the peer has a mailbox now: its waiting items go at once.
	if err := o.retryWaiting(ctx, peer.MachineID); err != nil {
		return err
	}
	switch st {
	case transport.SendQueued:
		if env.Kind.IsControl() {
			// Receivers never confirm control kinds with control.delivered, so a
			// queued control item would otherwise sit in the outbox (and in the
			// status counts) until the purge, and RequeueStale would resend it
			// after every RelayTTL. The relay now owns it.
			return o.deleteSending(ctx, it.ID)
		}
		// The relay keeps the frame for RelayTTL; if no control.delivered arrives by
		// then, SendDue moves the item back to pending and it is sent again.
		_, err := o.outbox.SetStatusIf(ctx, it.ID, store.OutboxSending, store.OutboxQueued, it.Attempts+1, o.clock.Now().Add(core.RelayTTL))
		return err
	case transport.SendNotAllowed:
		if o.clock.Now().Sub(peer.PairedAt) < PairingGrace {
			attempts := it.Attempts + 1
			delay := min(backoffDelay(attempts), PairingGraceRetry)
			_, err := o.outbox.SetStatusIf(ctx, it.ID, store.OutboxSending, store.OutboxPending, attempts, o.clock.Now().Add(delay))
			return err
		}
		if err := o.markPausedByPeer(ctx, peer); err != nil { // holds this item too
			return err
		}
		if env.Kind.IsControl() {
			// Keep retrying: the peer may have paused us after we paused them, and
			// only our control.resumed tells it we are back.
			return o.backoffSending(ctx, it)
		}
		return nil
	case transport.SendTooLarge:
		o.recordError(fmt.Sprintf("dropped message %s to %s: too large for the relay", it.ID, peer.Alias))
		return o.deleteSending(ctx, it.ID)
	case transport.SendQueueFull:
		o.recordError(fmt.Sprintf("peer's mailbox full: %s", peer.Alias))
		return o.backoffSending(ctx, it)
	default: // rate_limited and anything unknown
		return o.backoffSending(ctx, it)
	}
}

// waitForMailbox handles a relay's unknown_mailbox: the item is retried
// with a backoff capped at NoMailboxRetryMax until it expires, and only
// then dropped and reported.
func (o *Outbound) waitForMailbox(ctx context.Context, peer store.Peer, it store.OutboxItem) error {
	if !o.clock.Now().Before(it.CreatedAt.Add(core.OutboxRetention)) {
		o.unwait(peer.MachineID, it.ID)
		o.recordError(noMailboxDrop(it.ID, peer.Alias))
		return o.deleteSending(ctx, it.ID)
	}
	attempts := it.Attempts + 1
	next := o.clock.Now().Add(backoffDelayMax(attempts, NoMailboxRetryMax))
	applied, err := o.outbox.SetStatusIf(ctx, it.ID, store.OutboxSending, store.OutboxPending, attempts, next)
	if err != nil || !applied {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	w, ok := o.waiting[peer.MachineID]
	if !ok {
		w = &noMailbox{items: map[string]time.Time{}}
		o.waiting[peer.MachineID] = w
	}
	w.alias = peer.Alias
	w.items[it.ID] = it.CreatedAt
	return nil
}

func noMailboxDrop(id, alias string) string {
	return fmt.Sprintf("dropped message %s: %s has no mailbox on this relay", id, alias)
}

// WithdrawUnsent deletes a message that is still waiting for the peer to
// have a relay mailbox and reports whether it did: the message never left.
// A message that is being sent, was sent, or waits for another reason is
// left alone.
func (o *Outbound) WithdrawUnsent(ctx context.Context, peer core.MachineID, msgID string) (bool, error) {
	o.mu.Lock()
	waiting := false
	if w, ok := o.waiting[peer]; ok {
		_, waiting = w.items[msgID]
	}
	o.mu.Unlock()
	if !waiting {
		return false, nil
	}
	deleted, err := o.outbox.DeleteIf(ctx, msgID, store.OutboxPending)
	if err != nil || !deleted {
		return false, err
	}
	o.unwait(peer, msgID)
	return true, nil
}

// unwait forgets one waiting item.
func (o *Outbound) unwait(peer core.MachineID, id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if w, ok := o.waiting[peer]; ok {
		delete(w.items, id)
		if len(w.items) == 0 {
			delete(o.waiting, peer)
		}
	}
}

// retryWaiting makes the peer's waiting items due now (those still pending)
// and forgets them.
func (o *Outbound) retryWaiting(ctx context.Context, peer core.MachineID) error {
	o.mu.Lock()
	w, ok := o.waiting[peer]
	delete(o.waiting, peer)
	o.mu.Unlock()
	if !ok {
		return nil
	}
	now := o.clock.Now()
	for id := range w.items {
		it, err := o.outbox.Get(ctx, id)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := o.outbox.SetStatusIf(ctx, id, store.OutboxPending, store.OutboxPending, it.Attempts, now); err != nil {
			return err
		}
	}
	o.Wake()
	return nil
}

// sendingTimeout is how long an item may stay marked in flight before
// RequeueStale returns it to pending (a daemon that stopped mid-send).
const sendingTimeout = 2 * time.Minute

// backoffSending schedules a retry for an item whose send is still in flight.
func (o *Outbound) backoffSending(ctx context.Context, it store.OutboxItem) error {
	attempts := it.Attempts + 1
	_, err := o.outbox.SetStatusIf(ctx, it.ID, store.OutboxSending, store.OutboxPending, attempts, o.clock.Now().Add(backoffDelay(attempts)))
	return err
}

// deleteSending drops an item whose send is still in flight.
func (o *Outbound) deleteSending(ctx context.Context, id string) error {
	_, err := o.outbox.DeleteIf(ctx, id, store.OutboxSending)
	return err
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
func backoffDelay(attempts int) time.Duration { return backoffDelayMax(attempts, core.BackoffMax) }

// backoffDelayMax is BackoffMin doubled per failed attempt, capped at limit.
func backoffDelayMax(attempts int, limit time.Duration) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := core.BackoffMin
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= limit {
			return limit
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

// Hold stops sending to a peer: its pending and queued items become held (control.* excepted).
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
	o.mu.Lock()
	delete(o.waiting, peer)
	o.mu.Unlock()
	return o.outbox.DeleteOutboxForPeer(ctx, peer)
}

// PurgeOld deletes items older than core.OutboxRetention. Items that were
// still waiting for the peer's mailbox are reported as dropped.
func (o *Outbound) PurgeOld(ctx context.Context) (int, error) {
	cutoff := o.clock.Now().Add(-core.OutboxRetention)
	n, err := o.outbox.PurgeOutboxBefore(ctx, cutoff)
	if err != nil {
		return n, err
	}
	var dropped []string
	o.mu.Lock()
	for peer, w := range o.waiting {
		for id, created := range w.items {
			if created.Before(cutoff) {
				dropped = append(dropped, noMailboxDrop(id, w.alias))
				delete(w.items, id)
			}
		}
		if len(w.items) == 0 {
			delete(o.waiting, peer)
		}
	}
	o.mu.Unlock()
	for _, msg := range dropped {
		o.recordError(msg)
	}
	return n, nil
}

// Errors returns recent delivery problems, oldest first, then one line per
// peer whose items wait for it to have a relay mailbox, for status.
func (o *Outbound) Errors() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := append([]string(nil), o.recent...)
	waiting := make([]string, 0, len(o.waiting))
	for _, w := range o.waiting {
		waiting = append(waiting, fmt.Sprintf("%s has no mailbox on this relay yet; %d messages waiting", w.alias, len(w.items)))
	}
	slices.Sort(waiting)
	return append(out, waiting...)
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

// encodeNoHTML marshals v as JSON without escaping '<', '>' and '&'. The
// outbox copy of an envelope must use it: json.Marshal would store each of
// those characters in the body as six bytes, and a full-size message of them
// would then be too large to seal.
func encodeNoHTML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
