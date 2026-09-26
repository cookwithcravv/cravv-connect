package e2e

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// fileSHA256 hashes a file by streaming it.
func fileSHA256(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// The largest file allowed (core.MaxFileBytes, exactly) goes through the real
// relay and arrives intact. The source is a sparse file with markers at the
// start, a chunk boundary and the end, so it is never held in memory.
func TestMaxSizeFileTransfer(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{BTrustsA: core.TrustAutonomous})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	src := filepath.Join(a.Proj, "max.bin")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(core.MaxFileBytes); err != nil {
		t.Fatal(err)
	}
	for _, off := range []int64{0, 50*core.FileChunkBytes - 3, core.MaxFileBytes - 16} {
		if _, err := f.WriteAt([]byte("MAXFILE-MARKER!!"), off); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(src); fi.Size() != core.MaxFileBytes {
		t.Fatalf("source size %d", fi.Size())
	}
	want := fileSHA256(t, src)

	start := time.Now()
	var sent ipc.FileSendResult
	Call(t, sa, ipc.MethodFileSend, ipc.FileSendParams{To: "bob", Path: "max.bin"}, &sent)
	item, _ := WaitItem(t, sb, 5*time.Minute, "100 MB file at bob", func(it ipc.InboxView) bool {
		return it.Kind == "file" && it.FileID == sent.FileID && it.Path != ""
	})
	t.Logf("100 MB transfer took %s", time.Since(start).Round(time.Millisecond))
	if fi, err := os.Stat(item.Path); err != nil || fi.Size() != core.MaxFileBytes {
		t.Fatalf("received %v, %v", fi, err)
	}
	if fileSHA256(t, item.Path) != want {
		t.Fatal("received file hash differs")
	}
}
