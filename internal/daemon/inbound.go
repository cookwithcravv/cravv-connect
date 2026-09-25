package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

// receiptFlushEvery bounds how long a control.delivered receipt waits for batching.
const receiptFlushEvery = 200 * time.Millisecond

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
	logger   *slog.Logger

	dropped atomic.Int64 // corrupt, unverifiable, stale, or unroutable frames
	skewed  atomic.Int64 // frames rejected for a timestamp too far in the future

	mu      sync.Mutex
	receipt map[core.MachineID][]string // envelope IDs awaiting control.delivered
	retry   map[string]bool             // IDs whose handler asked for a retry
}

// NewInbound wires an Inbound. logger may be nil.
func NewInbound(id *keys.Identity, peers store.PeerStore, dedup store.DedupStore, prekeys PrekeySource,
	registry *HandlerRegistry, sender EnvelopeSender, clock core.Clock, logger *slog.Logger) *Inbound {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Inbound{
		identity: id, peers: peers, dedup: dedup, prekeys: prekeys, registry: registry,
		sender: sender, clock: clock, logger: logger,
		receipt: make(map[core.MachineID][]string),
		retry:   make(map[string]bool),
	}
}

// Dropped counts frames dropped as corrupt, unverifiable, stale, or unroutable (for status).
func (in *Inbound) Dropped() int64 { return in.dropped.Load() }

// SkewRejected counts frames rejected for timestamps too far in the future (status warns).
func (in *Inbound) SkewRejected() int64 { return in.skewed.Load() }

// Run consumes deliveries until the channel closes or ctx ends. Receipts are flushed
// when a burst of deliveries ends and at least every 200ms.
func (in *Inbound) Run(ctx context.Context, mb transport.Mailbox) error {
	ticker := time.NewTicker(receiptFlushEvery)
	defer ticker.Stop()
	defer in.flushReceipts(context.WithoutCancel(ctx))
	deliveries := mb.Deliveries()
	holdAcks := false // set after a retryable failure: acks are cumulative
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
			if !in.process(ctx, d) {
				holdAcks = true
			}
			if !holdAcks {
				if err := mb.Ack(ctx, d.Seq); err != nil {
					in.logger.Warn("ack failed", "seq", d.Seq, "err", err)
				}
			}
			if len(deliveries) == 0 {
				in.flushReceipts(ctx)
			}
		}
	}
}

// process handles one delivery. It returns false only when the delivery must not be acked.
func (in *Inbound) process(ctx context.Context, d transport.Delivery) bool {
	peer, err := in.peers.GetPeer(ctx, keys.MachineIDOf(d.From))
	if err != nil {
		in.drop("unknown sender", d, err)
		return true
	}
	if peer.Paused {
		in.drop("paused peer", d, nil)
		return true
	}
	frame, err := sealing.ParseFrame(d.Frame)
	if err != nil {
		in.drop("unparseable frame", d, err)
		return true
	}
	env, err := sealing.Open(frame, peer.IK, in.identity.MachineID(), in.prekeys)
	if errors.Is(err, sealing.ErrUnknownPrekey) {
		in.replyStalePrekey(ctx, peer, frame.Header.ID)
		return true
	}
	if err != nil {
		in.drop("unverifiable frame", d, err)
		return true
	}
	if err := in.checkTimestamp(env); err != nil {
		in.drop("bad timestamp", d, err)
		return true
	}
	seen, err := in.dedup.SeenOrMark(ctx, env.ID, in.clock.Now())
	if err != nil {
		in.logger.Error("dedup store failed", "id", env.ID, "err", err)
		return false
	}
	if seen && !in.takeRetry(env.ID) {
		if !env.Kind.IsControl() {
			in.queueReceipt(peer.MachineID, env.ID)
		}
		return true
	}
	h, ok := in.registry.Lookup(env.Kind)
	if !ok {
		in.drop("no handler for kind "+string(env.Kind), d, nil)
		return true
	}
	if err := h.Handle(ctx, peer, env); err != nil {
		var re *RetryableError
		if errors.As(err, &re) {
			in.logger.Warn("handler failed, will retry", "kind", env.Kind, "id", env.ID, "err", err)
			in.markRetry(env.ID)
			return false
		}
		in.logger.Warn("handler failed", "kind", env.Kind, "id", env.ID, "err", err)
		return true
	}
	if !env.Kind.IsControl() {
		in.queueReceipt(peer.MachineID, env.ID)
	}
	return true
}

func (in *Inbound) checkTimestamp(env core.Envelope) error {
	ts := time.UnixMilli(env.TS)
	now := in.clock.Now()
	if now.Sub(ts) > core.MaxMessageAge {
		return fmt.Errorf("message %s is older than %s", env.ID, core.MaxMessageAge)
	}
	if ts.Sub(now) > core.MaxClockSkew {
		in.skewed.Add(1)
		return fmt.Errorf("message %s is %s in the future", env.ID, ts.Sub(now).Round(time.Second))
	}
	return nil
}

func (in *Inbound) replyStalePrekey(ctx context.Context, peer store.Peer, msgID string) {
	cur, err := in.prekeys.Current(ctx)
	if err != nil {
		in.logger.Error("no current prekey for stale_prekey reply", "err", err)
		return
	}
	body := core.StalePrekeyBody{MsgID: msgID, Prekey: cur.Wire()}
	if _, err := in.sender.SendEnvelope(ctx, peer.MachineID, core.KindControlStalePrekey, "", "", body); err != nil {
		in.logger.Warn("stale_prekey reply failed", "peer", peer.Alias, "err", err)
	}
}

func (in *Inbound) drop(reason string, d transport.Delivery, err error) {
	in.dropped.Add(1)
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
		if _, err := in.sender.SendEnvelope(ctx, peer, core.KindControlDelivered, "", "", core.DeliveredBody{IDs: ids}); err != nil {
			in.logger.Warn("delivered receipt failed", "peer", peer.Short(), "err", err)
		}
	}
}

func (in *Inbound) markRetry(id string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.retry[id] = true
}

// takeRetry reports whether id failed retryably before, and clears the mark.
func (in *Inbound) takeRetry(id string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.retry[id] {
		return false
	}
	delete(in.retry, id)
	return true
}
