package daemon

import (
	"context"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

// hookFiles is a FileSender that runs hook before each upload.
type hookFiles struct {
	d2Files
	hook func()
}

func (f *hookFiles) SendFile(ctx context.Context, to core.MachineID, projectDir, path, taskID string) (core.FileRef, error) {
	if f.hook != nil {
		f.hook()
	}
	return f.d2Files.SendFile(ctx, to, projectDir, path, taskID)
}

// Complete takes the task before uploading result files: a cancel that lands
// during the upload finds it done, and the result (with its files) goes out.
func TestCompleteTransitionsBeforeUploading(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	files := &hookFiles{}
	e.tasks.d.Files = files
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id := e.incoming(t, peer, "", "work")
	if _, err := e.tasks.Claim(ctx, "claude@proj", id); err != nil {
		t.Fatal(err)
	}
	files.hook = func() {
		env := d2Env(t, peer, core.KindTaskCancel, "", "", core.TaskCancelBody{TaskID: id})
		if err := e.tasks.HandleCancel(ctx, peer, env); err != nil {
			t.Error(err)
		}
	}
	tk, err := e.tasks.Complete(ctx, "claude@proj", "/w/proj", id, "42", []string{"out.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if tk.State != core.TaskDone || len(tk.ResultFiles) != 1 {
		t.Fatalf("returned %+v", tk)
	}
	if got := e.state(t, id); got.State != core.TaskDone || len(got.ResultFiles) != 1 {
		t.Fatalf("stored %s files %v", got.State, got.ResultFiles)
	}
	ups := d2Updates(t, e.sender)
	last := ups[len(ups)-1]
	if last.State != core.TaskDone || last.Result != "42" || len(last.Files) != 1 {
		t.Fatalf("update = %+v", last)
	}
}
