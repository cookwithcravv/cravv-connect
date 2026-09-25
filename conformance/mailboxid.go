package conformance

import (
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

func mailboxOf(s transport.Signer) core.MachineID {
	return core.MachineID(relayproto.MailboxID(s.Public()))
}
