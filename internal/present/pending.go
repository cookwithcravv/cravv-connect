package present

import (
	"fmt"
	"strings"
)

// Pending is one group of items waiting for a shared session: Count items of
// Kind that arrived on local link Link from the machine the user calls
// Machine. Nothing in it is chosen by the peer.
type Pending struct {
	Link    int64
	Machine string
	Kind    string
	Count   int
}

// Pending kinds.
const (
	PendingMessage    = "message"
	PendingTask       = "task"
	PendingTaskUpdate = "task_update"
	PendingFile       = "file"
	PendingLink       = "link"     // a link was accepted, rejected or closed
	PendingRequest    = "request"  // a link request waits for a human decision
	PendingApproval   = "approval" // a tasks-ask task waits for a human decision
)

// IsDecision reports whether items of kind wait for a human decision
// (review_pending), which the Stop hook never blocks on.
func IsDecision(kind string) bool { return kind == PendingRequest || kind == PendingApproval }

// maxPendingSegments caps how many groups PendingLine names.
const maxPendingSegments = 3

var pendingNouns = map[string][2]string{
	PendingMessage:    {"new message", "new messages"},
	PendingTask:       {"new task", "new tasks"},
	PendingTaskUpdate: {"new task update", "new task updates"},
	PendingFile:       {"new file", "new files"},
	PendingLink:       {"link notice", "link notices"},
	PendingRequest:    {"link request", "link requests"},
	PendingApproval:   {"task awaiting approval", "tasks awaiting approval"},
}

// PendingLine renders the one line the listener prints and the hooks show,
// naming only local aliases and link numbers. Examples:
//
//	cravv-connect: 1 new task on link 2 from gpu-box. Call check_inbox.
//	cravv-connect: 2 new messages on link 2 from gpu-box, 1 link request on link 3 from laptop. Call check_inbox, then review_pending.
//
// It returns "" when nothing is pending.
func PendingLine(ps []Pending) string {
	var segs []string
	more, decisions := 0, false
	for _, p := range ps {
		if p.Count <= 0 {
			continue
		}
		decisions = decisions || IsDecision(p.Kind)
		if len(segs) == maxPendingSegments {
			more += p.Count
			continue
		}
		noun, ok := pendingNouns[p.Kind]
		if !ok {
			noun = [2]string{"new item", "new items"}
		}
		segs = append(segs, fmt.Sprintf("%d %s on link %d from %s", p.Count, plural(p.Count, noun[0], noun[1]), p.Link, cleanAttr(p.Machine)))
	}
	if len(segs) == 0 {
		return ""
	}
	if more > 0 {
		segs = append(segs, fmt.Sprintf("%d more %s", more, plural(more, "item", "items")))
	}
	tail := "Call check_inbox."
	if decisions {
		tail = "Call check_inbox, then review_pending."
	}
	return "cravv-connect: " + strings.Join(segs, ", ") + ". " + tail
}
