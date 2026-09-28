package app

import (
	"github.com/cookwithcravv/cravv-connect/internal/auth"
	"github.com/cookwithcravv/cravv-connect/internal/daemon"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/store"
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
		{daemon.ErrBadSessionName, ipc.KindBadRequest},
		{daemon.ErrBadPurpose, ipc.KindBadRequest},
		{daemon.ErrBadVisibility, ipc.KindBadRequest},
		{daemon.ErrAlreadyShared, ipc.KindBadRequest},
		{daemon.ErrBadPermission, ipc.KindBadRequest},
		{daemon.ErrBadNote, ipc.KindBadRequest},
		{daemon.ErrBadTarget, ipc.KindBadRequest},
		{daemon.ErrBadReviewItem, ipc.KindBadRequest},
		{daemon.ErrBadCode, ipc.KindBadCode},
		{daemon.ErrCodeLocked, ipc.KindCodeLocked},
		{daemon.ErrNoDesktop, ipc.KindNoDesktop},
		{daemon.ErrNoDecision, ipc.KindNoDecision},
		{daemon.ErrReviewRateLimited, ipc.KindRateLimited},
		{store.ErrNameTaken, ipc.KindBadRequest},
		{daemon.ErrDiscoveryTimeout, KindOffline},
		{daemon.ErrOffline, KindOffline},
		{daemon.ErrOfflineForPairing, KindOffline},
		{daemon.ErrPairingFailed, KindPairingFailed},
		{daemon.ErrPairingPeerOutdated, KindPairingFailed},
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
