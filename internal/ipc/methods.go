package ipc

import (
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Method names (ipc-v1). The gate for each is set where it is registered
// (internal/api) and documented in the plan's C7 table.
const (
	MethodSessionRegister = "session.register"
	MethodStatus          = "status"
	MethodChatSend        = "chat.send"
	MethodInboxCheck      = "inbox.check"
	MethodInboxWait       = "inbox.wait"
	MethodTaskCreate      = "task.create"
	MethodTaskGet         = "task.get"
	MethodTaskClaim       = "task.claim"
	MethodTaskUpdate      = "task.update"
	MethodTaskComplete    = "task.complete"
	MethodTaskFail        = "task.fail"
	MethodTaskCancel      = "task.cancel"
	MethodFileSend        = "file.send"
	MethodPeerList        = "peer.list"
	MethodPeerPause       = "peer.pause"
	MethodPeerResume      = "peer.resume"
	MethodPeerUnpair      = "peer.unpair"
	MethodPeerAlias       = "peer.alias"
	MethodKill            = "kill"
	MethodResume          = "resume"
	MethodAuthUnlock      = "auth.unlock"
	MethodPairStart       = "pair.start"
	MethodPairAwait       = "pair.await"
	MethodJoinStart       = "join.start"
	MethodPairFinalize    = "pair.finalize"
	MethodApprovalsList   = "approvals.list"
	MethodApprovalsDecide = "approvals.decide"
	MethodFilesList       = "files.list"
	MethodFilesAccept     = "files.accept"
	MethodAllowPathAdd    = "allow_path.add"
	MethodResetIdentity   = "reset_identity"
	MethodAuditRead       = "audit.read"
	MethodHookCounts      = "hook.counts"
	MethodDaemonShutdown  = "daemon.shutdown"

	// v2: shared sessions, discovery and links.
	MethodSessionShare    = "session.share"
	MethodSessionClose    = "session.close"
	MethodSessionSet      = "session.set"
	MethodSessionReattach = "session.reattach"
	MethodSessionListen   = "session.listen"
	MethodMachines        = "machines"
	MethodSessionsList    = "sessions.list"
	MethodLinkConnect     = "link.connect"
	MethodLinks           = "links"
	MethodLinkDisconnect  = "link.disconnect"
	MethodLinkRestrict    = "link.restrict"
	MethodLinkPermit      = "link.permit"
	MethodLinkDecide      = "link.decide"

	// v2 Phase 2: decisions in chat (review_pending).
	MethodReviewList   = "review.list"
	MethodReviewDecide = "review.decide"
	MethodReviewCode   = "review.code"
)

// Empty is the params or result of methods that carry nothing ({}).
type Empty struct{}

type SessionRegisterParams struct {
	Agent      string `json:"agent"`
	ProjectDir string `json:"project_dir"`
	PID        int    `json:"pid"`
}
type SessionRegisterResult struct {
	Name string `json:"name"`
}

// ChatSendParams sends chat on a link of the session shared on this connection.
type ChatSendParams struct {
	Link int64  `json:"link"`
	Text string `json:"text"`
}
type IDResult struct {
	ID string `json:"id"`
}

type InboxCheckParams struct {
	Limit int `json:"limit"`
}
type InboxWaitParams struct {
	TimeoutS int `json:"timeout_s"`
}
type InboxResult struct {
	Items []InboxView `json:"items"`
}

type TaskCreateParams struct {
	Link         int64    `json:"link"`
	Instructions string   `json:"instructions"`
	FilePaths    []string `json:"file_paths,omitempty"`
}
type TaskCreateResult struct {
	TaskID string `json:"task_id"`
}
type TaskIDParams struct {
	TaskID string `json:"task_id"`
}
type TaskUpdateParams struct {
	TaskID string `json:"task_id"`
	Note   string `json:"note"`
}
type TaskCompleteParams struct {
	TaskID    string   `json:"task_id"`
	Result    string   `json:"result"`
	FilePaths []string `json:"file_paths,omitempty"`
}
type TaskFailParams struct {
	TaskID string `json:"task_id"`
	Reason string `json:"reason"`
}

type FileSendParams struct {
	Link int64  `json:"link"`
	Path string `json:"path"`
}
type FileSendResult struct {
	FileID string `json:"file_id"`
}

type PeerListResult struct {
	Peers []PeerView `json:"peers"`
}
type AliasParams struct {
	Alias string `json:"alias"`
}
type PeerAliasParams struct {
	Alias    string `json:"alias"`
	NewAlias string `json:"new_alias"`
}

type UnlockParams struct {
	Password string `json:"password"`
}
type UnlockResult struct {
	ExpiresAt time.Time `json:"expires_at"`
}

type PairStartResult struct {
	PendingID string `json:"pending_id"`
	Code      string `json:"code"`
}
type PairAwaitParams struct {
	PendingID string `json:"pending_id"`
}
type PendingPeerResult struct {
	PendingID     string `json:"pending_id"`
	SuggestedName string `json:"suggested_name"`
	MachineID     string `json:"machine_id"`
}
type JoinStartParams struct {
	Code string `json:"code"`
}
type PairFinalizeParams struct {
	PendingID string `json:"pending_id"`
	Alias     string `json:"alias"`
}
type PairFinalizeResult struct {
	Alias string `json:"alias"`
}

type ApprovalsListResult struct {
	Tasks []ApprovalView `json:"tasks"`
}
type ApprovalsDecideParams struct {
	TaskID  string `json:"task_id"`
	Approve bool   `json:"approve"`
}

type FilesListResult struct {
	Files []FileView `json:"files"`
}
type FileIDParams struct {
	FileID string `json:"file_id"`
}

type AllowPathParams struct {
	Path string `json:"path"`
}

type AuditReadParams struct {
	Limit int `json:"limit"`
}
type AuditReadResult struct {
	Events []audit.Event `json:"events"`
}

type HookCountsParams struct {
	Cwd string `json:"cwd"`
}

// HookCountsResult carries the ready-made notice line plus the raw counts so
// hook callers can decide per event (for example, Stop only cares about Unread).
type HookCountsResult struct {
	Notice    string `json:"notice"`
	Unread    int    `json:"unread"`
	Approvals int    `json:"approvals"`
}

// InboxView is one delivered item. Wrapped is the only field agents should read
// as content.
type InboxView struct {
	Seq     int64     `json:"seq"`
	ID      string    `json:"id"`
	From    string    `json:"from"`
	Session string    `json:"session,omitempty"`
	Link    int64     `json:"link,omitempty"`
	Kind    string    `json:"kind"`
	TaskID  string    `json:"task_id,omitempty"`
	FileID  string    `json:"file_id,omitempty"`
	Path    string    `json:"path,omitempty"`
	Wrapped string    `json:"wrapped"`
	At      time.Time `json:"at"`
}

// TaskView is a task as agents see it. Text written by the peer (an inbound
// task's instructions and file names; an outbound task's result, the peer's
// progress notes and result file names) is never returned raw: it is
// rendered once, inside a <remote_message> wrapper, in Wrapped, and the raw
// fields are left empty. Instructions, Result and Notes hold only text this
// machine wrote.
type TaskView struct {
	TaskID       string           `json:"task_id"`
	Direction    string           `json:"direction"`
	Peer         string           `json:"peer"`
	State        string           `json:"state"`
	ClaimedBy    string           `json:"claimed_by,omitempty"`
	Result       string           `json:"result,omitempty"`
	Instructions string           `json:"instructions,omitempty"`
	Notes        []store.TaskNote `json:"notes,omitempty"`
	Files        []core.FileRef   `json:"files,omitempty"`
	ResultFiles  []core.FileRef   `json:"result_files,omitempty"`
	Wrapped      string           `json:"wrapped,omitempty"`
	UpdatedAt    time.Time        `json:"updated_at"`
}

type PeerView struct {
	Alias        string    `json:"alias"`
	MachineID    string    `json:"machine_id"`
	Online       bool      `json:"online"`
	Paused       bool      `json:"paused"`
	PausedByPeer bool      `json:"paused_by_peer"`
	PairedAt     time.Time `json:"paired_at"`
	// LastSeen is when the peer was last heard from since the daemon
	// started; zero (omitted) if it has not been.
	LastSeen time.Time `json:"last_seen,omitzero"`
}

type ApprovalView struct {
	TaskID   string    `json:"task_id"`
	Peer     string    `json:"peer"`
	Preview  string    `json:"preview"`
	SHA256   string    `json:"sha256"`
	Size     int       `json:"size"`
	Received time.Time `json:"received"`
	Full     string    `json:"full"`
}

type FileView struct {
	FileID    string `json:"file_id"`
	Direction string `json:"direction"`
	Peer      string `json:"peer"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Path      string `json:"path,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Size      int64  `json:"size"`
}

type StatusResult struct {
	MachineID        string     `json:"machine_id"`
	DeviceName       string     `json:"device_name"`
	RelayURL         string     `json:"relay_url"`
	RelayConnected   bool       `json:"relay_connected"`
	Killed           bool       `json:"killed"`
	Peers            []PeerView `json:"peers"`
	Sessions         []string   `json:"sessions"`
	OutboxPending    int        `json:"outbox_pending"`
	OutboxHeld       int        `json:"outbox_held"`
	InboxUnread      int        `json:"inbox_unread"`
	PendingApprovals int        `json:"pending_approvals"`
	Errors           []string   `json:"errors,omitempty"`
}

// SessionShareParams shares the chat on this connection. Visibility is
// "private" (the default), "all-peers" or "peers:<alias>[,<alias>...]".
type SessionShareParams struct {
	Name       string `json:"name"`
	Purpose    string `json:"purpose,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// SharedSessionView is a local shared session. It never carries its ID.
type SharedSessionView struct {
	Name       string `json:"name"`
	Purpose    string `json:"purpose,omitempty"`
	Visibility string `json:"visibility"`
	State      string `json:"state"`
	Kind       string `json:"kind"`
	Agent      string `json:"agent"`
}

// ShareResult carries the two secrets for the client that shared: the wake
// token for the listener (counts only) and the reattach token for taking the
// session back after a reconnect. Neither may be put on a command line.
type ShareResult struct {
	Session       SharedSessionView `json:"session"`
	WakeToken     string            `json:"wake_token"`
	ReattachToken string            `json:"reattach_token"`
}

// SessionSetParams changes the session; a nil field is left unchanged.
type SessionSetParams struct {
	Purpose    *string `json:"purpose,omitempty"`
	Visibility *string `json:"visibility,omitempty"`
}

type SessionReattachParams struct {
	ReattachToken string `json:"reattach_token"`
}

// SessionListenParams blocks until the session holding the wake token has
// something pending, or for TimeoutS seconds (0: until the connection ends).
type SessionListenParams struct {
	WakeToken string `json:"wake_token"`
	TimeoutS  int    `json:"timeout_s,omitempty"`
}

// ListenResult is counts plus local names only (aliases and link
// numbers): never bodies, peer-chosen names or IDs. Closed means the
// session closed while listening.
type ListenResult struct {
	Unread    int            `json:"unread"`
	Requests  int            `json:"requests"`
	Approvals int            `json:"approvals,omitempty"`
	Pending   []PendingCount `json:"pending,omitempty"`
	Closed    bool           `json:"closed,omitempty"`
}

// PendingCount is Count unread items of Kind (message, task, task_update,
// file, link, request or approval) on local link Link from the machine the
// user calls Machine.
type PendingCount struct {
	Link    int64  `json:"link"`
	Machine string `json:"machine"`
	Kind    string `json:"kind"`
	Count   int    `json:"count"`
}

type MachineParams struct {
	Machine string `json:"machine"`
}

// RemoteSessionView is a session a peer lets this machine see. Name is
// validated ([a-z0-9-]); the peer's free-text purpose is only in Wrapped.
type RemoteSessionView struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Agent   string `json:"agent,omitempty"`
	State   string `json:"state"`
	Wrapped string `json:"wrapped,omitempty"`
}

type SessionsListResult struct {
	Machine  string              `json:"machine"`
	Sessions []RemoteSessionView `json:"sessions"`
}

// LinkConnectParams asks target ("machine/session") for a link from the
// session shared on this connection; Permission is what this side proposes
// to do there.
type LinkConnectParams struct {
	Target     string `json:"target"`
	Permission string `json:"permission"`
	Note       string `json:"note,omitempty"`
}

type LinkParams struct {
	Link int64 `json:"link"`
}

type LinkPermissionParams struct {
	Link       int64  `json:"link"`
	Permission string `json:"permission"`
}

// LinkDecideParams accepts or rejects a pending request. Permission is the
// level granted ("" grants what was proposed).
type LinkDecideParams struct {
	Link       int64  `json:"link"`
	Accept     bool   `json:"accept"`
	Permission string `json:"permission,omitempty"`
}

// LinkView is one link. RemoteSession is validated ([a-z0-9-]); the peer's
// free-text purpose and request note are only in Wrapped.
type LinkView struct {
	Link          int64  `json:"link"`
	Machine       string `json:"machine"`
	Session       string `json:"session"`
	RemoteSession string `json:"remote_session"`
	Direction     string `json:"direction"`
	State         string `json:"state"`
	PermissionIn  string `json:"permission_in,omitempty"`
	PermissionOut string `json:"permission_out,omitempty"`
	Proposed      string `json:"proposed,omitempty"`
	RemoteAway    bool   `json:"remote_away,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Wrapped       string `json:"wrapped,omitempty"`
}

type LinksResult struct {
	Links []LinkView `json:"links"`
}

// ReviewItemView is one decision waiting for the human of the session
// shared on this connection. Item is "link-<number>" or "task-<task id>".
// Session is the remote session name (validated); Permission is what a
// link request asks for, or what a task's link allows. Wrapped holds the
// peer's text for the human's form only (a link's purpose and note, a
// task's instructions): the MCP server never shows a task's to the model.
type ReviewItemView struct {
	Item       string `json:"item"`
	Kind       string `json:"kind"` // link | task
	Link       int64  `json:"link"`
	Machine    string `json:"machine"`
	Session    string `json:"session"`
	Permission string `json:"permission"`
	Wrapped    string `json:"wrapped,omitempty"`
}

type ReviewListResult struct {
	Items []ReviewItemView `json:"items"`
}

// ReviewDecideParams applies the human's answer. With Code empty it is an
// answer from an elicitation form; with Code set, the confirmation code
// the human typed (needed to accept, not to reject). Permission is the
// level granted when accepting a link ("" means what was asked, at most
// tasks-ask for a code).
type ReviewDecideParams struct {
	Item       string `json:"item"`
	Accept     bool   `json:"accept"`
	Permission string `json:"permission,omitempty"`
	Code       string `json:"code,omitempty"`
}

// ReviewDecideResult says what happened: accepted or rejected (links),
// approved or denied (tasks). Permission is what an accepted link allows.
type ReviewDecideResult struct {
	Item       string `json:"item"`
	Outcome    string `json:"outcome"`
	Link       int64  `json:"link"`
	Permission string `json:"permission,omitempty"`
}

type ReviewItemParams struct {
	Item string `json:"item"`
}
