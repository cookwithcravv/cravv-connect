package conformance

import (
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

func mailboxOf(s transport.Signer) core.MachineID {
	return core.MachineID(relayproto.MailboxID(s.Public()))
}
