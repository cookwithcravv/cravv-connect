package daemon

import (
	"context"
	"log/slog"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// LinkReplies sends the two rate-limited answers to traffic this machine
// will not take: link.closed{unknown_link} for a link it does not have open
// (at most one per link per core.UnknownLinkReplyEvery), and
// control.unsupported for v1 traffic without a link_id (at most one per peer
// per core.UnsupportedReplyEvery). Both go out directly, best effort: they
// answer the peer's traffic, so they are sent again if the peer keeps sending.
type LinkReplies struct {
	sender      DirectSender
	unknown     *RateLimiter
	unsupported *RateLimiter
	log         *slog.Logger
}

// NewLinkReplies wires the replier. log may be nil.
func NewLinkReplies(sender DirectSender, clock core.Clock, log *slog.Logger) *LinkReplies {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &LinkReplies{
		sender:      sender,
		unknown:     NewRateLimiter(clock, 1, core.UnknownLinkReplyEvery),
		unsupported: NewRateLimiter(clock, 1, core.UnsupportedReplyEvery),
		log:         log,
	}
}

// UnknownLink tells the peer that linkID is not open here, so it closes its
// side (v2 spec 5, split brain). Invalid IDs are never answered.
func (r *LinkReplies) UnknownLink(ctx context.Context, peer store.Peer, linkID string) {
	if !core.ValidID(linkID) || !r.unknown.Allow(string(peer.MachineID)+"/"+linkID) {
		return
	}
	body := core.LinkClosedBody{LinkID: linkID, Reason: core.CloseUnknownLink}
	if err := r.sender.SendDirect(ctx, peer, core.KindLinkClosed, body); err != nil {
		r.log.Info("unknown_link reply not sent", "peer", peer.Alias, "err", err)
	}
}

// Unsupported tells a v1 peer that this machine needs link-scoped traffic.
func (r *LinkReplies) Unsupported(ctx context.Context, peer store.Peer) {
	if !r.unsupported.Allow(string(peer.MachineID)) {
		return
	}
	body := core.UnsupportedBody{MinVersion: core.ProtocolVersion}
	if err := r.sender.SendDirect(ctx, peer, core.KindControlUnsupported, body); err != nil {
		r.log.Info("control.unsupported reply not sent", "peer", peer.Alias, "err", err)
	}
}
