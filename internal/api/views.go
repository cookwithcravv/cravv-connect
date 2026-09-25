package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
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

func (h *handlers) taskView(ctx context.Context, t store.Task) ipc.TaskView {
	alias, _ := h.peerLabel(ctx, t.Peer)
	return ipc.TaskView{
		TaskID: t.ID, Direction: string(t.Direction), Peer: alias, State: string(t.State),
		ClaimedBy: t.ClaimedBy, Result: t.Result, Instructions: t.Instructions,
		Notes: t.Notes, Files: t.Files, ResultFiles: t.ResultFiles, UpdatedAt: t.UpdatedAt,
	}
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
