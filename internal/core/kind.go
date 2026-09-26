package core

import "strings"

// Kind names the type of a sealed envelope.
type Kind string

const (
	KindChat               Kind = "chat"
	KindTaskCreate         Kind = "task.create"
	KindTaskUpdate         Kind = "task.update"
	KindTaskCancel         Kind = "task.cancel"
	KindFileOffer          Kind = "file.offer"
	KindControlPrekey      Kind = "control.prekey"
	KindControlStalePrekey Kind = "control.stale_prekey"
	KindControlDelivered   Kind = "control.delivered"
	KindControlPaused      Kind = "control.paused"
	KindControlResumed     Kind = "control.resumed"
	KindControlUnpaired    Kind = "control.unpaired"
	KindControlRelayMoved  Kind = "control.relay_moved"
	KindControlUnsupported Kind = "control.unsupported"

	KindSessionsList   Kind = "sessions.list"
	KindSessionsListed Kind = "sessions.listed"
	KindLinkRequest    Kind = "link.request"
	KindLinkAccepted   Kind = "link.accepted"
	KindLinkRejected   Kind = "link.rejected"
	KindLinkClosed     Kind = "link.closed"
	KindLinkState      Kind = "link.state"
	KindPresencePing   Kind = "presence.ping"
	KindPresencePong   Kind = "presence.pong"
)

// kindTraits is the registry of per-kind delivery rules. Kinds not listed
// have none of the traits.
var kindTraits = map[Kind]struct{ linkScoped, ephemeral bool }{
	KindChat:           {linkScoped: true},
	KindTaskCreate:     {linkScoped: true},
	KindTaskUpdate:     {linkScoped: true},
	KindTaskCancel:     {linkScoped: true},
	KindFileOffer:      {linkScoped: true},
	KindSessionsList:   {ephemeral: true},
	KindSessionsListed: {ephemeral: true},
	KindPresencePing:   {ephemeral: true},
	KindPresencePong:   {ephemeral: true},
}

// IsControl reports whether k is a daemon-to-daemon control kind.
func (k Kind) IsControl() bool { return strings.HasPrefix(string(k), "control.") }

// LinkScoped reports whether k must carry a link_id: chat, task.* and
// file.offer are only accepted over an active link.
func (k Kind) LinkScoped() bool { return kindTraits[k].linkScoped }

// Ephemeral reports whether k is sent directly, never through the outbox:
// it is never receipted, retried or deduplicated, and a receiver drops it
// when it is older than PresenceMaxAge (a frame the relay queued while this
// machine was offline expires harmlessly).
func (k Kind) Ephemeral() bool { return kindTraits[k].ephemeral }

// Receipted reports whether a receiver confirms k with control.delivered:
// every kind except control.* and ephemeral kinds.
func (k Kind) Receipted() bool { return !k.IsControl() && !k.Ephemeral() }
