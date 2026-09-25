package ipc

import (
	"errors"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
)

// Errors that exist only at the IPC layer.
var (
	// ErrBadRequest marks malformed or invalid parameters.
	ErrBadRequest = errors.New("bad request")
	// ErrDaemonNotRunning is returned by Dial when nothing listens on the socket.
	ErrDaemonNotRunning = errors.New("daemon not running: run `cravv-connect daemon start`")
	// ErrClosed is returned by Call when the connection has ended.
	ErrClosed = errors.New("connection to daemon closed")
	// ErrBusy is returned when a connection already has MaxInflight requests
	// running. Retry after one of them finishes.
	ErrBusy = errors.New("too many requests in flight on this connection")
)

// Error kinds carried in Error.Data.Kind.
const (
	KindNotFound       = "not_found"
	KindNotPermitted   = "not_permitted"
	KindPaused         = "paused"
	KindPausedByPeer   = "paused_by_peer"
	KindKilled         = "killed"
	KindAuthRequired   = "auth_required"
	KindLocked         = "locked"
	KindBadPassword    = "bad_password"
	KindAlreadyClaimed = "already_claimed"
	KindBadTransition  = "bad_transition"
	KindTooLarge       = "too_large"
	KindPathRefused    = "path_refused"
	KindQuota          = "quota"
	KindNoSession      = "no_session"
	KindBadRequest     = "bad_request"
	KindBusy           = "busy"
	KindInternal       = "internal"
)

type errorKind struct {
	kind string
	err  error
}

// errorKinds is the single table both directions of the mapping use. Built-in
// kinds come first; RegisterErrorKind appends more.
var (
	kindsMu    sync.RWMutex
	errorKinds = []errorKind{
		{KindNotFound, core.ErrNotFound},
		{KindNotPermitted, core.ErrNotPermitted},
		{KindPausedByPeer, core.ErrPausedByPeer},
		{KindPaused, core.ErrPaused},
		{KindKilled, core.ErrKilled},
		{KindAuthRequired, core.ErrAuthRequired},
		{KindLocked, core.ErrLocked},
		{KindBadPassword, core.ErrBadPassword},
		{KindAlreadyClaimed, core.ErrAlreadyClaimed},
		{KindBadTransition, core.ErrBadTransition},
		{KindTooLarge, core.ErrTooLarge},
		{KindPathRefused, core.ErrPathRefused},
		{KindQuota, core.ErrQuota},
		{KindNoSession, core.ErrNoSession},
		{KindBadRequest, ErrBadRequest},
		{KindBusy, ErrBusy},
	}
)

// RegisterErrorKind maps an extra sentinel error to a wire kind, in both
// directions. Packages that must not be imported by ipc (the daemon) register
// their errors through internal/app. Several errors may share one kind; the
// first registered one is what the client side unwraps to.
func RegisterErrorKind(err error, kind string) {
	if err == nil || kind == "" {
		panic("ipc: RegisterErrorKind needs an error and a kind")
	}
	kindsMu.Lock()
	defer kindsMu.Unlock()
	for _, e := range errorKinds {
		if e.err == err {
			return
		}
	}
	errorKinds = append(errorKinds, errorKind{kind, err})
}

// IsKind reports whether err is a daemon error of the given kind. It works for
// kinds this process has no sentinel for.
func IsKind(err error, kind string) bool {
	var re *RemoteError
	if errors.As(err, &re) {
		return re.Kind == kind
	}
	return KindOf(err) == kind
}

// KindOf returns the error kind for err, or KindInternal when err wraps none
// of the known sentinels.
func KindOf(err error) string {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	for _, e := range errorKinds {
		if errors.Is(err, e.err) {
			return e.kind
		}
	}
	return KindInternal
}

// sentinelFor returns the sentinel error for kind, or nil for unknown kinds.
func sentinelFor(kind string) error {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	for _, e := range errorKinds {
		if e.kind == kind {
			return e.err
		}
	}
	return nil
}

// RemoteError is an error returned by the daemon. errors.Is matches the
// sentinel error of its kind; kinds unknown to this process keep Kind and
// Message and unwrap to nothing.
type RemoteError struct {
	Kind    string
	Message string
}

func (e *RemoteError) Error() string { return e.Message }

// Unwrap returns the sentinel error for the kind (nil for internal errors).
func (e *RemoteError) Unwrap() error { return sentinelFor(e.Kind) }

// toWire converts a handler error into a JSON-RPC error object.
func toWire(err error) *Error {
	kind := KindOf(err)
	code := CodeServerError
	if kind == KindBadRequest {
		code = CodeInvalidParams
	}
	return &Error{Code: code, Message: err.Error(), Data: &ErrorData{Kind: kind}}
}

// fromWire converts a JSON-RPC error object into a Go error.
func fromWire(e *Error) error {
	kind := KindInternal
	if e.Data != nil && e.Data.Kind != "" {
		kind = e.Data.Kind
	} else if e.Code == CodeInvalidParams || e.Code == CodeMethodNotFound || e.Code == CodeParseError {
		kind = KindBadRequest
	}
	return &RemoteError{Kind: kind, Message: e.Message}
}
