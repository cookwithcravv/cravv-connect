package app

import (
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// Wire kinds for daemon and auth errors that have no core sentinel.
const (
	KindAuthUnavailable = "auth_unavailable"
	KindOffline         = "offline"
	KindPairingFailed   = "pairing_failed"
	KindPairingExpired  = "pairing_expired"
	KindBusy            = ipc.KindBusy
)

// INTEGRATION SEAM: the daemon's exported errors (Tasks 13-20). Registered at
// init so both the daemon process and every client binary (which imports this
// package through internal/cli) map them the same way.
func init() {
	for _, e := range []struct {
		err  error
		kind string
	}{
		{daemon.ErrBadAlias, ipc.KindBadRequest},
		{daemon.ErrOffline, KindOffline},
		{daemon.ErrOfflineForPairing, KindOffline},
		{daemon.ErrPairingFailed, KindPairingFailed},
		{daemon.ErrPairingExpired, KindPairingExpired},
		{daemon.ErrPairingInProgress, KindBusy},
		{daemon.ErrPairingClosed, KindOffline},
		{auth.ErrUnavailable, KindAuthUnavailable},
		{auth.ErrServiceNotAllowed, KindAuthUnavailable},
		{auth.ErrAcceptsAnyPassword, KindAuthUnavailable},
	} {
		ipc.RegisterErrorKind(e.err, e.kind)
	}
}
