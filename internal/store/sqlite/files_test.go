package sqlite

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func testFile(id string, dir store.TaskDirection, peer core.MachineID, st store.FileState, size int64, created time.Time) store.FileRecord {
	return store.FileRecord{
		FileID: id, Direction: dir, Peer: peer, MsgID: "m-" + id, BlobID: "b-" + id, Name: id + ".bin",
		Size: size, Chunks: 3, SHA256: bytes.Repeat([]byte{1}, 32), Key: bytes.Repeat([]byte{2}, 32),
		State: st, CreatedAt: created,
	}
}

func TestFileRoundTripAndUpdate(t *testing.T) {
	ctx := context.Background()
	fs := newTestDB(t)
	f := testFile("f1", store.TaskInbound, "A", store.FileOffered, 100, t0)
	f.TaskID = "t1"
	if err := fs.PutFile(ctx, f); err != nil {
		t.Fatal(err)
	}
	got, err := fs.GetFile(ctx, "f1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Chunks != 3 || got.TaskID != "t1" || !bytes.Equal(got.Key, f.Key) || !bytes.Equal(got.SHA256, f.SHA256) ||
		got.BlobID != "b-f1" || got.MsgID != "m-f1" || !got.CreatedAt.Equal(t0) {
		t.Fatalf("round trip = %+v", got)
	}
	up, err := fs.UpdateFile(ctx, "f1", func(r *store.FileRecord) error {
		r.State = store.FileDownloading
		r.NextChunk = 2
		r.Attempts = 1
		r.LocalPath = "/x/files/a/m-f1-f1.bin"
		return nil
	})
	if err != nil || up.State != store.FileDownloading || up.NextChunk != 2 {
		t.Fatalf("UpdateFile = %+v, %v", up, err)
	}
	got, _ = fs.GetFile(ctx, "f1")
	if got.NextChunk != 2 || got.Attempts != 1 || got.LocalPath != "/x/files/a/m-f1-f1.bin" {
		t.Fatalf("after update = %+v", got)
	}
	boom := errors.New("boom")
	if _, err := fs.UpdateFile(ctx, "f1", func(r *store.FileRecord) error { r.State = store.FileFailed; return boom }); !errors.Is(err, boom) {
		t.Fatalf("mutate error = %v", err)
	}
	if got, _ = fs.GetFile(ctx, "f1"); got.State != store.FileDownloading {
		t.Fatalf("aborted update written: %s", got.State)
	}
	if _, err := fs.UpdateFile(ctx, "missing", func(*store.FileRecord) error { return nil }); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing UpdateFile err = %v", err)
	}
	if _, err := fs.GetFile(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing GetFile err = %v", err)
	}
}

func TestListFilesByState(t *testing.T) {
	ctx := context.Background()
	fs := newTestDB(t)
	for _, f := range []store.FileRecord{
		testFile("f2", store.TaskInbound, "A", store.FileHeld, 1, t0.Add(time.Second)),
		testFile("f1", store.TaskInbound, "A", store.FileOffered, 1, t0),
		testFile("f3", store.TaskOutbound, "B", store.FileSent, 1, t0.Add(2*time.Second)),
	} {
		if err := fs.PutFile(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(states ...store.FileState) []string {
		list, err := fs.ListFiles(ctx, states...)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(list))
		for i, f := range list {
			out[i] = f.FileID
		}
		return out
	}
	if got := ids(); !equalStrings(got, []string{"f1", "f2", "f3"}) {
		t.Fatalf("all = %v", got)
	}
	if got := ids(store.FileHeld, store.FileSent); !equalStrings(got, []string{"f2", "f3"}) {
		t.Fatalf("held+sent = %v", got)
	}
}

func TestInboundBytesCountsOnlyInboundDownloadingOrDone(t *testing.T) {
	ctx := context.Background()
	fs := newTestDB(t)
	for _, f := range []store.FileRecord{
		testFile("dl", store.TaskInbound, "A", store.FileDownloading, 1000, t0),
		testFile("done", store.TaskInbound, "A", store.FileDone, 200, t0),
		testFile("held", store.TaskInbound, "A", store.FileHeld, 50000, t0),
		testFile("failed", store.TaskInbound, "A", store.FileFailed, 70000, t0),
		testFile("offered", store.TaskInbound, "A", store.FileOffered, 80000, t0),
		testFile("out", store.TaskOutbound, "A", store.FileSent, 90000, t0),
		testFile("other-peer", store.TaskInbound, "B", store.FileDone, 7, t0),
	} {
		if err := fs.PutFile(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	n, err := fs.InboundBytes(ctx, "A")
	if err != nil || n != 1200 {
		t.Fatalf("InboundBytes(A) = %d, %v; want 1200", n, err)
	}
	if n, _ := fs.InboundBytes(ctx, "nobody"); n != 0 {
		t.Fatalf("InboundBytes(nobody) = %d", n)
	}
}
