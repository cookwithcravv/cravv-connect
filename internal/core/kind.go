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
)

// IsControl reports whether k is a daemon-to-daemon control kind.
func (k Kind) IsControl() bool { return strings.HasPrefix(string(k), "control.") }
