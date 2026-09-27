// Package audit records security-relevant events (pairing, links,
// approvals, password attempts, file transfers) as append-only JSONL.
package audit

import (
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// Event is one audit log line. It never carries message bodies or secrets;
// content is referenced by Hash (hex SHA-256).
type Event struct {
	TS     time.Time      `json:"ts"`
	Type   string         `json:"type"`
	Peer   core.MachineID `json:"peer,omitempty"`
	Alias  string         `json:"alias,omitempty"`
	ItemID string         `json:"item_id,omitempty"`
	Hash   string         `json:"hash,omitempty"` // hex sha256 of content
	Detail map[string]any `json:"detail,omitempty"`
}

// Event types.
const (
	EvPair          = "pair"
	EvUnpair        = "unpair"
	EvPause         = "pause"
	EvResume        = "resume"
	EvKill          = "kill"
	EvKillResume    = "kill_resume"
	EvApprove       = "approve"
	EvDeny          = "deny"
	EvPassword      = "password_attempt"
	EvTaskIn        = "task_in"
	EvFileIn        = "file_in"
	EvFileOut       = "file_out"
	EvAllowPath     = "allow_path"
	EvResetIdentity = "reset_identity"
	EvFileAccept    = "file_accept"

	EvLinkRequest    = "link_request"
	EvLinkAccept     = "link_accept"
	EvLinkReject     = "link_reject"
	EvLinkClose      = "link_close"
	EvLinkPermission = "link_permission"
)

// Logger records audit events.
type Logger interface{ Record(e Event) error }

// Nop discards events (tests and tools that must not write the log).
type Nop struct{}

func (Nop) Record(Event) error { return nil }
