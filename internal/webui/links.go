package webui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// permissions are the link levels, lowest first.
var permissions = []core.Permission{core.PermMessages, core.PermTasksAsk, core.PermTasksAuto}

// linkRow is a link as the pages show it.
type linkRow struct {
	ipc.LinkView
	StateText string
	TheyMay   string
	Peer      string            // the peer's purpose and note, cleaned
	Lower     []core.Permission // levels the human may restrict to
}

func newLinkRow(l ipc.LinkView) linkRow {
	r := linkRow{LinkView: l, Peer: peerText(l.Wrapped)}
	switch {
	case l.State == "pending" && l.Direction == "in":
		r.StateText = "request (you decide)"
	case l.State == "pending":
		r.StateText = "requested (they decide)"
	case l.State == "closed":
		r.StateText = "closed: " + cleanLine(l.Reason)
	case l.Unreachable:
		r.StateText = "away (peer machine not answering)"
	case l.RemoteAway:
		r.StateText = "active (peer away)"
	default:
		r.StateText = cleanLine(l.State)
	}
	r.TheyMay = dash(l.PermissionIn)
	if l.State == "pending" && l.Direction == "in" {
		r.TheyMay = "asks " + l.Proposed
	}
	if l.State == "active" {
		for _, p := range permissions {
			if p.Below(core.Permission(l.PermissionIn)) {
				r.Lower = append(r.Lower, p)
			}
		}
	}
	return r
}

// isRequest reports whether l is a link request waiting for this side.
func isRequest(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" }

// formLink reads the link number of a form.
func formLink(rq *Request) (int64, error) {
	n, err := strconv.ParseInt(rq.Form("link"), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: link must be a link number", ipc.ErrBadRequest)
	}
	return n, nil
}

// peerLines cleans multi-line peer text (a task's instructions) line by line.
func peerLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = cleanLine(strings.ReplaceAll(l, "\t", "    "))
	}
	return strings.Join(lines, "\n")
}
