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
	MethodPeerTrust       = "peer.trust"
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

type ChatSendParams struct {
	To   string `json:"to"`
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
	To           string   `json:"to"`
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
	To   string `json:"to"`
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
type PeerTrustParams struct {
	Alias string `json:"alias"`
	Level string `json:"level"`
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
	Trust     string `json:"trust"`
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
	TrustIn      string    `json:"trust_in"`
	Online       bool      `json:"online"`
	Paused       bool      `json:"paused"`
	PausedByPeer bool      `json:"paused_by_peer"`
	PairedAt     time.Time `json:"paired_at"`
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
