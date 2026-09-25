// Package api registers the ipc-v1 method handlers. It depends only on the
// narrow ports below, never on concrete daemon types; internal/app adapts the
// daemon to these ports.
package api

import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

// SessionPort registers and ends agent sessions.
type SessionPort interface {
	Register(ctx context.Context, agent, projectDir string) (string, error)
	Disconnect(ctx context.Context, name string) error
}

// ChatPort sends chat. to is "alias" or "alias/session".
type ChatPort interface {
	Send(ctx context.Context, fromSession, to, text string) (string, error)
}

// InboxPort reads a session's inbox and advances its read position. The
// daemon renders each item (local alias, trust, escaped <remote_message>
// wrapper), so the port returns finished views.
type InboxPort interface {
	Check(ctx context.Context, session string, limit int) ([]ipc.InboxView, error)
	Wait(ctx context.Context, session string, timeout time.Duration) ([]ipc.InboxView, error)
}

// TaskPort covers both task directions and the human approval queue.
type TaskPort interface {
	Create(ctx context.Context, session, projectDir, to, instructions string, filePaths []string) (string, error)
	Get(ctx context.Context, session, id string) (store.Task, error)
	Claim(ctx context.Context, session, id string) (store.Task, error)
	Update(ctx context.Context, session, id, note string) (store.Task, error)
	Complete(ctx context.Context, session, projectDir, id, result string, filePaths []string) (store.Task, error)
	Fail(ctx context.Context, session, id, reason string) (store.Task, error)
	Cancel(ctx context.Context, session, id string) (store.Task, error)
	Approvals(ctx context.Context) ([]store.Task, error)
	// Decide approves or denies a held task. unlocked reports whether this
	// IPC connection holds a fresh password unlock; the daemon re-checks it.
	Decide(ctx context.Context, id string, approve, unlocked bool) error
}

// FilePort sends, lists, and accepts files. to is "alias" or "alias/session".
type FilePort interface {
	Send(ctx context.Context, to, projectDir, path string) (core.FileRef, error)
	// Accept releases a held file. unlocked is the connection's unlock state.
	Accept(ctx context.Context, id string, unlocked bool) error
	List(ctx context.Context) ([]store.FileRecord, error)
}

// PeerPort looks up and controls paired peers.
type PeerPort interface {
	ByAlias(ctx context.Context, alias string) (store.Peer, error)
	ByID(ctx context.Context, id core.MachineID) (store.Peer, error)
	Pause(ctx context.Context, alias string) error
	Resume(ctx context.Context, alias string) error
	Unpair(ctx context.Context, alias string) error
	Rename(ctx context.Context, alias, newAlias string) error
	// SetTrust changes the trust level. unlocked reports whether this IPC
	// connection holds a fresh password unlock; raising trust requires it.
	SetTrust(ctx context.Context, alias string, level core.TrustLevel, unlocked bool) error
}

// PairingPort runs the bind-code flow on both sides.
type PairingPort interface {
	Start(ctx context.Context) (ipc.PairStartResult, error)
	Await(ctx context.Context, pendingID string) (ipc.PendingPeerResult, error)
	Join(ctx context.Context, code string) (ipc.PendingPeerResult, error)
	Finalize(ctx context.Context, pendingID, alias string, trust core.TrustLevel) (string, error)
}

// ControlPort holds machine-wide controls. AddAllowPath validates the
// directory and audits the change. The human-only methods take unlocked, the
// IPC connection's unlock state, so the daemon enforces the gate as well.
type ControlPort interface {
	Killed() bool
	Kill(ctx context.Context) error
	Resume(ctx context.Context, unlocked bool) error
	AddAllowPath(ctx context.Context, dir string, unlocked bool) error
	ResetIdentity(ctx context.Context, unlocked bool) error
}

// LifecyclePort stops the daemon process. Shutdown returns at once; the
// daemon stops shortly after, once the reply has been written.
type LifecyclePort interface {
	Shutdown() error
}

// StatusPort reports machine status, including peers with their Online flag.
type StatusPort interface {
	Status(ctx context.Context) (ipc.StatusResult, error)
}

// AuditPort reads the audit log.
type AuditPort interface {
	Read(ctx context.Context, limit int) ([]audit.Event, error)
}

// HookPort returns unread counts keyed by local alias for the session
// registered in cwd, plus the number of tasks awaiting approval.
type HookPort interface {
	Counts(ctx context.Context, cwd string) (unread map[string]int, approvals int, err error)
}

// AuthPort checks the OS login password (implemented by *auth.Guard, which
// rate-limits and audits every attempt).
type AuthPort interface {
	Check(password string) error
}

// Ports aggregates every port the API needs.
type Ports struct {
	Sessions SessionPort
	Chat     ChatPort
	Inbox    InboxPort
	Tasks    TaskPort
	Files    FilePort
	Peers    PeerPort
	Pairing  PairingPort
	Control  ControlPort
	Status   StatusPort
	Audit    AuditPort
	Hook     HookPort
	Auth     AuthPort
	// Lifecycle is optional: nil means daemon.shutdown is not supported.
	Lifecycle LifecyclePort
}
