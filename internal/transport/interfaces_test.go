package transport_test

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// keys.Identity must satisfy Signer so the daemon can dial with it directly.
var _ transport.Signer = (*keys.Identity)(nil)

// Mailbox is exactly FrameConn plus RelayControl, so code that needs only one half
// can depend on the narrower interface and still accept any Mailbox.
var (
	_ transport.FrameConn    = transport.Mailbox(nil)
	_ transport.RelayControl = transport.Mailbox(nil)
	_ transport.Mailbox      = struct {
		transport.FrameConn
		transport.RelayControl
	}{}
)

func TestSendStatusMatchesWire(t *testing.T) {
	pairs := map[transport.SendStatus]string{
		transport.SendQueued:         relayproto.StatusQueued,
		transport.SendNotAllowed:     relayproto.StatusNotAllowed,
		transport.SendQueueFull:      relayproto.StatusQueueFull,
		transport.SendTooLarge:       relayproto.StatusTooLarge,
		transport.SendUnknownMailbox: relayproto.StatusUnknownMailbox,
		transport.SendRateLimited:    relayproto.StatusRateLimited,
	}
	for s, wire := range pairs {
		if string(s) != wire {
			t.Errorf("SendStatus %q != wire %q", s, wire)
		}
	}
}

func TestSentinelsDistinct(t *testing.T) {
	if errors.Is(transport.ErrRelayForbidden, transport.ErrRoomGone) {
		t.Fatal("sentinels must be distinct")
	}
	var d transport.Delivery
	d.From = ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	if len(d.From) != 32 {
		t.Fatal("unexpected key size")
	}
}
