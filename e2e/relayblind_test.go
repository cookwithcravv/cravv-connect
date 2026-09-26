package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/relayserver"
)

// recordingBackend keeps a copy of every frame queued and every blob chunk
// stored, i.e. everything the relay operator could read.
type recordingBackend struct {
	relayserver.Backend
	mu     sync.Mutex
	frames [][]byte
	chunks [][]byte
}

func (b *recordingBackend) Enqueue(ctx context.Context, mailbox string, from ed25519.PublicKey, id string, frame []byte, lim relayserver.QueueLimits) (uint64, error) {
	b.mu.Lock()
	b.frames = append(b.frames, append([]byte(id+"\x00"), frame...))
	b.mu.Unlock()
	return b.Backend.Enqueue(ctx, mailbox, from, id, frame, lim)
}

func (b *recordingBackend) PutChunk(ctx context.Context, id string, n uint32, data []byte, maxTotal int64) error {
	b.mu.Lock()
	b.chunks = append(b.chunks, append([]byte(nil), data...))
	b.mu.Unlock()
	return b.Backend.PutChunk(ctx, id, n, data, maxTotal)
}

func (b *recordingBackend) snapshot() (frames, chunks [][]byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]byte(nil), b.frames...), append([][]byte(nil), b.chunks...)
}

// Spec 3: the relay sees only ciphertext. Nothing a user or agent wrote
// (chat, task text, results, file names and contents, device names) may
// appear in any frame the relay queues or any blob chunk it stores.
func TestRelayNeverSeesPlaintext(t *testing.T) {
	t.Parallel()
	rec := &recordingBackend{}
	r := NewRelayWith(t, func(b relayserver.Backend) relayserver.Backend { rec.Backend = b; return rec })
	a := NewNode(t, r, "zelda-q7k", NodeOptions{AdminToken: AdminToken})
	b := NewNode(t, r, "yorick-q7k", NodeOptions{})
	Pair(t, a, b, PairOptions{})
	l := LinkChats(t, a, b, a.Share("claude", "plainsess-a1x9", "private"), b.Share("codex", "plainsess-b2y8", "all-peers"), "tasks-auto")
	sa, sb := l.A.C, l.B.C

	const (
		chat    = "PLAINTEXT-CHAT-7f3a"
		instr   = "PLAINTEXT-TASK-9c1d"
		result  = "PLAINTEXT-RESULT-4e6b"
		name    = "plaintext-name-5e2c.txt"
		content = "PLAINTEXT-CONTENT-2b8f"
	)
	id := sendChat(t, sa, l.ANum, chat)
	WaitItem(t, sb, wait, "chat at bob", isChat(id))

	var created ipc.TaskCreateResult
	Call(t, sa, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: instr}, &created)
	WaitItem(t, sb, wait, "task at bob", func(it ipc.InboxView) bool { return it.TaskID == created.TaskID })
	Call(t, sb, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil)
	Call(t, sb, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: created.TaskID, Result: result}, nil)
	Eventually(t, wait, "result at alice", func() bool {
		var tv ipc.TaskView
		Call(t, sa, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		return tv.State == "done"
	})

	body := bytes.Repeat([]byte(content+"\n"), 3*core.FileChunkBytes/len(content))
	if err := os.WriteFile(filepath.Join(a.Proj, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
	var sent ipc.FileSendResult
	Call(t, sa, ipc.MethodFileSend, ipc.FileSendParams{Link: l.ANum, Path: name}, &sent)
	WaitItem(t, sb, wait, "file at bob", func(it ipc.InboxView) bool { return it.FileID == sent.FileID && it.Path != "" })

	frames, chunks := rec.snapshot()
	if len(frames) < 4 || len(chunks) < 3 {
		t.Fatalf("recorded %d frames and %d chunks: the recorder missed traffic", len(frames), len(chunks))
	}
	secrets := []string{chat, instr, result, name, content, "zelda-q7k", "yorick-q7k", "claude@proj", "codex@proj",
		"plainsess-a1x9", "plainsess-b2y8"}
	for _, blobs := range [][][]byte{frames, chunks} {
		for i, data := range blobs {
			for _, s := range secrets {
				if bytes.Contains(data, []byte(s)) {
					t.Errorf("relay saw %q in stored item %d", s, i)
				}
			}
		}
	}
}
