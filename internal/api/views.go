package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/cravv/cravv-connect/internal/store"
)

// PreviewRunes is how much of a pending task the approvals queue shows.
const PreviewRunes = 500

// PeerViewOf maps a peer record to its view. A peer is online when the relay
// connection is up and neither side has paused the other.
func PeerViewOf(p store.Peer, relayConnected bool) ipc.PeerView {
	return ipc.PeerView{
		Alias:        p.Alias,
		MachineID:    string(p.MachineID),
		TrustIn:      p.TrustIn.String(),
		Online:       relayConnected && !p.Paused && !p.PausedByPeer,
		Paused:       p.Paused,
		PausedByPeer: p.PausedByPeer,
		PairedAt:     p.PairedAt,
	}
}

// peerLabel returns the local alias and trust label for a machine. Unknown
// (for example unpaired) machines show their short machine ID, never a
// peer-chosen name.
func (h *handlers) peerLabel(ctx context.Context, id core.MachineID) (alias, trust string) {
	p, err := h.p.Peers.ByID(ctx, id)
	if err != nil {
		return id.Short(), "unpaired"
	}
	return p.Alias, p.TrustIn.String()
}

// taskView maps a task to its view. Peer-authored text is moved out of the
// raw fields into Wrapped (see ipc.TaskView): for an inbound task the
// instructions and file names; for an outbound task the result, the notes
// that arrived in task.update messages (they carry a MsgID) and the result
// file names.
func (h *handlers) taskView(ctx context.Context, t store.Task) ipc.TaskView {
	alias, trust := h.peerLabel(ctx, t.Peer)
	v := ipc.TaskView{
		TaskID: t.ID, Direction: string(t.Direction), Peer: alias, State: string(t.State),
		ClaimedBy: t.ClaimedBy, UpdatedAt: t.UpdatedAt,
	}
	var body strings.Builder
	section := func(title, text string) {
		if text == "" {
			return
		}
		if body.Len() > 0 {
			body.WriteString("\n\n")
		}
		body.WriteString(title)
		body.WriteString(":\n")
		body.WriteString(text)
	}
	kind, peerSession := "task", t.FromSession
	if t.Direction == store.TaskInbound {
		v.Result, v.Notes = t.Result, t.Notes
		section("Instructions", t.Instructions)
		section("Files", fileList(t.Files))
		v.Files = withoutNames(t.Files)
		v.ResultFiles = t.ResultFiles
	} else {
		kind, peerSession = "task_update", t.ClaimedBy
		// The claimer is the peer's session name, chosen by the peer.
		v.ClaimedBy = present.CleanAttr(t.ClaimedBy)
		v.Instructions, v.Files = t.Instructions, t.Files
		var peerNotes []string
		for _, n := range t.Notes {
			if n.MsgID == "" {
				v.Notes = append(v.Notes, n)
				continue
			}
			peerNotes = append(peerNotes, "- "+n.At.UTC().Format(time.RFC3339)+" "+n.Text)
		}
		section("Result", t.Result)
		section("Notes", strings.Join(peerNotes, "\n"))
		section("Result files", fileList(t.ResultFiles))
		v.ResultFiles = withoutNames(t.ResultFiles)
	}
	if body.Len() > 0 {
		v.Wrapped = present.Wrap(present.Item{
			Alias: alias, Session: peerSession, Trust: trust, ID: t.ID, Kind: kind, TaskID: t.ID, Body: body.String(),
		})
	}
	return v
}

// fileList renders file references one per line for a wrapped view.
func fileList(fs []core.FileRef) string {
	lines := make([]string, len(fs))
	for i, f := range fs {
		lines[i] = fmt.Sprintf("- %s (%d bytes, file_id %s)", f.Name, f.Size, f.FileID)
	}
	return strings.Join(lines, "\n")
}

// withoutNames copies file references with the peer-chosen names removed
// (they appear in the wrapped text instead).
func withoutNames(fs []core.FileRef) []core.FileRef {
	if fs == nil {
		return nil
	}
	out := make([]core.FileRef, len(fs))
	for i, f := range fs {
		out[i] = core.FileRef{FileID: f.FileID, Size: f.Size}
	}
	return out
}

func (h *handlers) fileView(ctx context.Context, f store.FileRecord) ipc.FileView {
	alias, _ := h.peerLabel(ctx, f.Peer)
	return ipc.FileView{
		FileID: f.FileID, Direction: string(f.Direction), Peer: alias, Name: f.Name,
		State: string(f.State), Path: f.LocalPath, Reason: f.Reason, Size: f.Size,
	}
}

func (h *handlers) approvalView(ctx context.Context, t store.Task) ipc.ApprovalView {
	alias, _ := h.peerLabel(ctx, t.Peer)
	sum := sha256.Sum256([]byte(t.Instructions))
	return ipc.ApprovalView{
		TaskID: t.ID, Peer: alias, Preview: truncateRunes(t.Instructions, PreviewRunes),
		SHA256: hex.EncodeToString(sum[:]), Size: len(t.Instructions), Received: t.CreatedAt,
		Full: t.Instructions,
	}
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
