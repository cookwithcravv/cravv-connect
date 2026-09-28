// Package api registers the ipc-v1 method handlers. It depends only on the
// narrow ports below, never on concrete daemon types; internal/app adapts the
// daemon to these ports.
package api

import (
	"context"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// SessionPort registers and ends agent sessions.
type SessionPort interface {
	Register(ctx context.Context, agent, projectDir string) (string, error)
	Disconnect(ctx context.Context, name string) error
}

// SharedPort manages shared sessions. A session is bound to the IPC
// connection (conn) that shared or reattached it; no method takes a session
// ID from a client.
type SharedPort interface {
	// Share creates a session for the attachment (agent, projectDir) on conn
	// and returns it with its ID (kept in the connection state, never sent).
	Share(ctx context.Context, conn uint64, agent, projectDir, name, purpose, visibility string) (id string, res ipc.ShareResult, err error)
	// Current fails unless session id is still bound to conn.
	Current(ctx context.Context, id string, conn uint64) error
	Close(ctx context.Context, id string) error
	Set(ctx context.Context, id string, purpose, visibility *string) (ipc.SharedSessionView, error)
	Reattach(ctx context.Context, conn uint64, token, agent, projectDir string) (id string, view ipc.SharedSessionView, err error)
	Detach(ctx context.Context, id string, conn uint64) error
	// Listen blocks until the session holding the wake token has something
	// pending (counts only), the timeout passes (0: none) or ctx ends.
	Listen(ctx context.Context, wakeToken string, timeout time.Duration) (ipc.ListenResult, error)
}

// DiscoveryPort lists the sessions a paired machine lets this one see.
type DiscoveryPort interface {
	Sessions(ctx context.Context, machine string) (ipc.SessionsListResult, error)
}

// LinkPort manages links. sessionID "" means every link (the human's CLI);
// otherwise only that shared session's links are visible.
type LinkPort interface {
	Connect(ctx context.Context, sessionID, target, permission, note string) (ipc.LinkView, error)
	List(ctx context.Context, sessionID string) ([]ipc.LinkView, error)
	Disconnect(ctx context.Context, sessionID string, link int64) error
	// Restrict lowers what the peer may do (no password).
	Restrict(ctx context.Context, sessionID string, link int64, permission string) (ipc.LinkView, error)
	// Permit sets any level; raising needs unlocked (the password).
	Permit(ctx context.Context, link int64, permission string, unlocked bool) (ipc.LinkView, error)
	// Decide accepts or rejects a pending request to sessionID ("" for any).
	// Accepting needs unlocked (the password path).
	Decide(ctx context.Context, sessionID string, link int64, accept bool, permission string, unlocked bool) (ipc.LinkView, error)
}

// ReviewPort serves review_pending for the shared session sessionID: the
// decisions waiting for its human, applied with the chat tier (never
// tasks-auto), and confirmation codes shown on the desktop only.
type ReviewPort interface {
	List(ctx context.Context, sessionID string) ([]ipc.ReviewItemView, error)
	Decide(ctx context.Context, sessionID string, p ipc.ReviewDecideParams) (ipc.ReviewDecideResult, error)
	ShowCode(ctx context.Context, sessionID, item string) error
}

// ChatPort sends chat on link number link of the shared session sessionID.
type ChatPort interface {
	Send(ctx context.Context, sessionID string, link int64, text string) (string, error)
}

// InboxPort reads a shared session's inbox and advances its read position.
// The daemon renders each item (local alias, link, permission, escaped
// <remote_message> wrapper), so the port returns finished views.
type InboxPort interface {
	Check(ctx context.Context, session string, limit int) ([]ipc.InboxView, error)
	Wait(ctx context.Context, session string, timeout time.Duration) ([]ipc.InboxView, error)
}

// TaskPort covers both task directions and the human approval queue. Every
// session argument is the shared session bound to the connection.
type TaskPort interface {
	Create(ctx context.Context, session, projectDir string, link int64, instructions string, filePaths []string) (string, error)
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

// FilePort sends and lists files. Send uses link number link of the
// shared session sessionID.
type FilePort interface {
	Send(ctx context.Context, sessionID string, link int64, projectDir, path string) (core.FileRef, error)
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
}

// PairingPort runs the bind-code flow on both sides.
// Start, Join and Finalize take unlocked, the IPC connection's unlock state,
// so the daemon enforces the human-only gate as well.
type PairingPort interface {
	Start(ctx context.Context, unlocked bool) (ipc.PairStartResult, error)
	Await(ctx context.Context, pendingID string) (ipc.PendingPeerResult, error)
	Join(ctx context.Context, code string, unlocked bool) (ipc.PendingPeerResult, error)
	Finalize(ctx context.Context, pendingID, alias string, unlocked bool) (string, error)
}

// ControlPort holds machine-wide controls. AddAllowPath validates the
// directory and audits the change. The human-only methods take unlocked, the
// IPC connection's unlock state, so the daemon enforces the gate as well.
type ControlPort interface {
	Killed() bool
	Kill(ctx context.Context) error
	Resume(ctx context.Context, unlocked bool) error
	AddAllowPath(ctx context.Context, dir string, unlocked bool) error
	// ResetIdentity returns the aliases of the peers that were not told.
	ResetIdentity(ctx context.Context, unlocked bool) (untold []string, err error)
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

// HookPort answers agent hooks for the shared session of the chat that
// runs them. Bind records the agent's chat ID for a session when the chat
// shares or reattaches it.
type HookPort interface {
	Check(ctx context.Context, q ipc.HookCountsParams) (ipc.HookCountsResult, error)
	Bind(ctx context.Context, agentSession, sessionID string)
}

// AuthPort checks the OS login password (implemented by *auth.Guard, which
// rate-limits and audits every attempt).
type AuthPort interface {
	Check(password string) error
}

// Ports aggregates every port the API needs.
type Ports struct {
	Sessions  SessionPort
	Shared    SharedPort
	Discovery DiscoveryPort
	Links     LinkPort
	Review    ReviewPort
	Chat      ChatPort
	Inbox     InboxPort
	Tasks     TaskPort
	Files     FilePort
	Peers     PeerPort
	Pairing   PairingPort
	Control   ControlPort
	Status    StatusPort
	Audit     AuditPort
	Hook      HookPort
	Auth      AuthPort
	// Lifecycle is optional: nil means daemon.shutdown is not supported.
	Lifecycle LifecyclePort
}
