package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/sealing"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// receiptFlushEvery bounds how long a control.delivered receipt waits for batching.
const receiptFlushEvery = 200 * time.Millisecond

// ErrRetryLater is returned by Inbound.Run after a handler failed retryably: the
// delivery was not acked, so the caller should reconnect (with backoff) and let the
// relay redeliver it.
var ErrRetryLater = errors.New("inbound: handler asked for a retry; reconnect to get a redelivery")

// RetryableError marks a handler failure that should be retried: the delivery is not acked,
// so the relay redelivers it on the next connection.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return "retryable: " + e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// Retryable wraps err as a *RetryableError (nil stays nil).
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err}
}

// Inbound turns relay deliveries into handled envelopes: identify the sender, verify and
// open the frame, check freshness, deduplicate, dispatch through the registry, confirm
// with control.delivered, and ack.
type Inbound struct {
	identity *keys.Identity
	peers    store.PeerStore
	dedup    store.DedupStore
	prekeys  PrekeySource
	registry *HandlerRegistry
	sender   EnvelopeSender
	clock    core.Clock
	killed   func() bool
	logger   *slog.Logger
	stale    *RateLimiter // control.stale_prekey replies per peer

	corrupt atomic.Int64 // unparseable or unverifiable frames, bad IDs and timestamps
	unknown atomic.Int64 // frames from machines that are not paired
	paused  atomic.Int64 // frames from machines this one paused
	skewed  atomic.Int64 // frames rejected for a timestamp too far in the future

	mu      sync.Mutex
	receipt map[core.MachineID][]string // envelope IDs awaiting control.delivered
}

// NewInbound wires an Inbound. killed may be nil (never killed); logger may be nil.
func NewInbound(id *keys.Identity, peers store.PeerStore, dedup store.DedupStore, prekeys PrekeySource,
	registry *HandlerRegistry, sender EnvelopeSender, clock core.Clock, killed func() bool, logger *slog.Logger) *Inbound {
	if killed == nil {
		killed = func() bool { return false }
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Inbound{
		identity: id, peers: peers, dedup: dedup, prekeys: prekeys, registry: registry,
		sender: sender, clock: clock, killed: killed, logger: logger,
		stale:   NewRateLimiter(clock, core.StalePrekeyRepliesPerMinute, time.Minute),
		receipt: make(map[core.MachineID][]string),
	}
}

// DropCounts counts dropped frames by why they were dropped (for status).
// Stale presence and discovery frames, expected after being offline, and
// kinds this version has no handler for are not counted.
type DropCounts struct {
	Corrupt int64 // unparseable, unverifiable, or with a bad ID or timestamp
	Unknown int64 // from a machine that is not paired
	Paused  int64 // from a machine this one paused
}

// Drops returns the dropped-frame counts since the daemon started.
func (in *Inbound) Drops() DropCounts {
	return DropCounts{Corrupt: in.corrupt.Load(), Unknown: in.unknown.Load(), Paused: in.paused.Load()}
}

// SkewRejected counts frames rejected for timestamps too far in the future (status warns).
func (in *Inbound) SkewRejected() int64 { return in.skewed.Load() }

// Run consumes deliveries until the channel closes or ctx ends. Receipts are flushed
// when a burst of deliveries ends and at least every 200ms. When a delivery cannot be
// handled now (a retryable handler failure or a dedup store error) it is not acked and
// Run returns an error wrapping ErrRetryLater: acks are cumulative, so the caller must
// reconnect and let the relay redeliver from that delivery on. When the kill switch is
// on, the delivery in hand is not handled or acked and Run returns core.ErrKilled.
func (in *Inbound) Run(ctx context.Context, mb transport.Mailbox) error {
	ticker := time.NewTicker(receiptFlushEvery)
	defer ticker.Stop()
	defer in.flushReceipts(context.WithoutCancel(ctx))
	deliveries := mb.Deliveries()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			in.flushReceipts(ctx)
		case d, ok := <-deliveries:
			if !ok {
				return nil
			}
			if in.killed() {
				return fmt.Errorf("inbound: seq %d not handled: %w", d.Seq, core.ErrKilled)
			}
			if err := in.process(ctx, d); err != nil {
				return fmt.Errorf("%w: seq %d: %v", ErrRetryLater, d.Seq, err)
			}
			if err := mb.Ack(ctx, d.Seq); err != nil {
				in.logger.Warn("ack failed", "seq", d.Seq, "err", err)
			}
			if len(deliveries) == 0 {
				in.flushReceipts(ctx)
			}
		}
	}
}

// process handles one delivery. It returns an error only when the delivery must not be
// acked (so it is redelivered); every other outcome, including drops, is acked.
//
// The dedup mark is written only once the handler succeeded or failed for good: a
// retryable failure leaves the ID unmarked, so a redelivery after a restart runs the
// handler again and no control.delivered is sent for a message that was not stored.
func (in *Inbound) process(ctx context.Context, d transport.Delivery) error {
	peer, err := in.peers.GetPeer(ctx, keys.MachineIDOf(d.From))
	if err != nil {
		in.drop(&in.unknown, "unknown sender", d, err)
		return nil
	}
	if peer.Paused {
		in.drop(&in.paused, "paused peer", d, nil)
		return nil
	}
	frame, err := sealing.ParseFrame(d.Frame)
	if err != nil {
		in.drop(&in.corrupt, "unparseable frame", d, err)
		return nil
	}
	env, err := sealing.Open(frame, peer.IK, in.identity.MachineID(), in.prekeys)
	if errors.Is(err, sealing.ErrUnknownPrekey) {
		// Only the header is authenticated here: check its ID like an
		// envelope's before answering, so replayed old frames get no reply.
		if err := in.checkIDTime(frame.Header.ID); err != nil {
			in.drop(&in.corrupt, "frame for an unknown prekey", d, err)
			return nil
		}
		in.replyStalePrekey(ctx, peer, frame.Header.ID)
		return nil
	}
	if err != nil {
		in.drop(&in.corrupt, "unverifiable frame", d, err)
		return nil
	}
	if !core.ValidID(env.ID) {
		in.drop(&in.corrupt, "message id is not a core ID", d, nil)
		return nil
	}
	if err := in.checkTimestamp(env); err != nil {
		in.drop(&in.corrupt, "bad timestamp", d, err)
		return nil
	}
	if env.Kind.Ephemeral() {
		in.processEphemeral(ctx, peer, env, d)
		return nil
	}
	seen, err := in.dedup.Seen(ctx, env.ID)
	if err != nil {
		in.logger.Error("dedup store failed", "id", env.ID, "err", err)
		return err
	}
	if seen {
		if env.Kind.Receipted() {
			in.queueReceipt(peer.MachineID, env.ID)
		}
		return nil
	}
	h, ok := in.registry.Lookup(env.Kind)
	if !ok {
		in.drop(nil, "no handler for kind "+string(env.Kind), d, nil)
		return nil
	}
	if err := h.Handle(ctx, peer, env); err != nil {
		var re *RetryableError
		if errors.As(err, &re) {
			in.logger.Warn("handler failed, will retry", "kind", env.Kind, "id", env.ID, "err", err)
			return err
		}
		// Received but unusable: confirm it anyway so the sender stops resending it.
		in.logger.Warn("handler failed", "kind", env.Kind, "id", env.ID, "err", err)
	}
	if _, err := in.dedup.SeenOrMark(ctx, env.ID, in.clock.Now()); err != nil {
		in.logger.Error("dedup mark failed", "id", env.ID, "err", err)
	}
	if env.Kind.Receipted() {
		in.queueReceipt(peer.MachineID, env.ID)
	}
	return nil
}

// processEphemeral handles presence and discovery frames: dropped when older
// than core.PresenceMaxAge (the relay may have queued them while this machine
// was offline), never deduplicated, receipted or retried.
func (in *Inbound) processEphemeral(ctx context.Context, peer store.Peer, env core.Envelope, d transport.Delivery) {
	if in.clock.Now().Sub(time.UnixMilli(env.TS)) > core.PresenceMaxAge {
		in.drop(nil, "stale "+string(env.Kind), d, nil) // expected after being offline
		return
	}
	h, ok := in.registry.Lookup(env.Kind)
	if !ok {
		in.drop(nil, "no handler for kind "+string(env.Kind), d, nil)
		return
	}
	if err := h.Handle(ctx, peer, env); err != nil {
		in.logger.Info("ephemeral handler failed", "kind", env.Kind, "id", env.ID, "err", err)
	}
}

func (in *Inbound) checkTimestamp(env core.Envelope) error {
	return in.checkTime(env.ID, time.UnixMilli(env.TS))
}

// checkIDTime applies checkTimestamp to the time a core ID encodes.
func (in *Inbound) checkIDTime(id string) error {
	ts, ok := core.IDTime(id)
	if !ok {
		return fmt.Errorf("message id %q is not a core ID", id)
	}
	return in.checkTime(id, ts)
}

// checkTime rejects message id sent at ts when it is older than
// core.MaxMessageAge or further than core.MaxClockSkew in the future.
func (in *Inbound) checkTime(id string, ts time.Time) error {
	now := in.clock.Now()
	if now.Sub(ts) > core.MaxMessageAge {
		return fmt.Errorf("message %s is older than %s", id, core.MaxMessageAge)
	}
	if ts.Sub(now) > core.MaxClockSkew {
		in.skewed.Add(1)
		return fmt.Errorf("message %s is %s in the future", id, ts.Sub(now).Round(time.Second))
	}
	return nil
}

// replyStalePrekey tells the sender which prekey to use, at most once per (peer, message ID):
// the relay may redeliver the frame, and each reply would otherwise trigger another resend.
// It answers at most core.StalePrekeyRepliesPerMinute frames per peer.
func (in *Inbound) replyStalePrekey(ctx context.Context, peer store.Peer, msgID string) {
	if !in.stale.Allow(string(peer.MachineID)) {
		in.logger.Info("stale_prekey reply rate limited", "peer", peer.Alias, "id", msgID)
		return
	}
	key := "stale:" + string(peer.MachineID) + ":" + msgID
	if seen, err := in.dedup.SeenOrMark(ctx, key, in.clock.Now()); err != nil {
		in.logger.Warn("stale_prekey dedup failed", "id", msgID, "err", err)
	} else if seen {
		return
	}
	cur, err := in.prekeys.Current(ctx)
	if err != nil {
		in.logger.Error("no current prekey for stale_prekey reply", "err", err)
		return
	}
	body := core.StalePrekeyBody{MsgID: msgID, Prekey: cur.Wire()}
	if _, err := in.sender.SendEnvelope(ctx, peer.MachineID, core.KindControlStalePrekey, "", body); err != nil {
		in.logger.Warn("stale_prekey reply failed", "peer", peer.Alias, "err", err)
	}
}

// drop logs a dropped delivery and adds it to counter; a nil counter is a
// drop that is expected and no reason for a status warning.
func (in *Inbound) drop(counter *atomic.Int64, reason string, d transport.Delivery, err error) {
	if counter != nil {
		counter.Add(1)
	}
	in.logger.Info("dropped delivery", "reason", reason, "seq", d.Seq, "id", d.ID, "err", err)
}

func (in *Inbound) queueReceipt(peer core.MachineID, id string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, have := range in.receipt[peer] {
		if have == id {
			return
		}
	}
	in.receipt[peer] = append(in.receipt[peer], id)
}

func (in *Inbound) flushReceipts(ctx context.Context) {
	in.mu.Lock()
	batch := in.receipt
	in.receipt = make(map[core.MachineID][]string)
	in.mu.Unlock()
	for peer, ids := range batch {
		if _, err := in.sender.SendEnvelope(ctx, peer, core.KindControlDelivered, "", core.DeliveredBody{IDs: ids}); err != nil {
			in.logger.Warn("delivered receipt failed", "peer", peer.Short(), "err", err)
		}
	}
}
