package core

import "errors"

// Sentinel errors shared across packages. Match them with errors.Is.
var (
	ErrNotFound       = errors.New("not found")
	ErrNotPermitted   = errors.New("not permitted by trust level")
	ErrPaused         = errors.New("peer is paused")
	ErrPausedByPeer   = errors.New("paused by peer")
	ErrKilled         = errors.New("kill switch is on")
	ErrAuthRequired   = errors.New("password required")
	ErrLocked         = errors.New("too many failed password attempts; locked")
	ErrBadPassword    = errors.New("incorrect password")
	ErrAlreadyClaimed = errors.New("task already claimed")
	ErrBadTransition  = errors.New("invalid task state transition")
	ErrTooLarge       = errors.New("too large")
	ErrPathRefused    = errors.New("path not allowed for sending")
	ErrQuota          = errors.New("file quota exceeded")
	ErrNoSession      = errors.New("no session registered on this connection")
)
