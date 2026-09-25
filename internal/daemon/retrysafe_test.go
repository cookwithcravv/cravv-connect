package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// flakyInbox fails the next `fail` AddItem calls, like a busy disk.
type flakyInbox struct {
	store.InboxStore
	mu   sync.Mutex
	fail int
}

func (f *flakyInbox) AddItem(ctx context.Context, it store.InboxItem) (int64, error) {
	f.mu.Lock()
	if f.fail > 0 {
		f.fail--
		f.mu.Unlock()
		return 0, errors.New("disk busy")
	}
	f.mu.Unlock()
	return f.InboxStore.AddItem(ctx, it)
}

func (f *flakyInbox) failNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = n
}

// d2FlakyTasks is d2Tasks with an inbox whose writes can be made to fail.
func d2FlakyTasks(t *testing.T) (*d2TaskEnv, *flakyInbox) {
	t.Helper()
	e := d2Tasks(t)
	flaky := &flakyInbox{InboxStore: e.st}
	e.inbox = NewInboxService(flaky, e.reg, e.st, e.clock)
	e.tasks = NewTaskService(TaskDeps{
		Tasks: e.st, Peers: e.st, Resolver: d2Resolver{e.st}, Inbox: e.inbox, Sender: e.sender,
		Policy: TrustPolicy{}, Files: e.files, Desktop: e.desktop, Clock: e.clock, Audit: e.audit,
	})
	return e, flaky
}

// handleRetry runs h once expecting a retryable failure, then again (the
// relay's redelivery) expecting success, then a third time (a duplicate).
func handleRetry(t *testing.T, flaky *flakyInbox, h func() error) {
	t.Helper()
	flaky.failNext(1)
	var re *RetryableError
	if err := h(); !errors.As(err, &re) {
		t.Fatalf("first attempt err = %v, want retryable", err)
	}
	for i := 0; i < 2; i++ {
		if err := h(); err != nil {
			t.Fatalf("attempt %d: %v", i+2, err)
		}
	}
}

func inboxFor(t *testing.T, e *d2TaskEnv, taskID string) []InboxEntry {
	t.Helper()
	session, err := e.reg.Register(context.Background(), "claude", "/w/fresh-"+taskID)
	if err != nil {
		t.Fatal(err)
	}
	all, err := e.inbox.Check(context.Background(), session, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []InboxEntry
	for _, it := range all {
		if it.Item.TaskID == taskID {
			out = append(out, it)
		}
	}
	return out
}

func TestHandleCreateRedeliversAfterRetryableFailure(t *testing.T) {
	ctx := context.Background()
	e, flaky := d2FlakyTasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id := core.NewID()
	env := d2Env(t, peer, core.KindTaskCreate, "codex@train", "", core.TaskCreateBody{TaskID: id, Instructions: "work"})
	handleRetry(t, flaky, func() error { return e.tasks.HandleCreate(ctx, peer, env) })
	if tk := e.state(t, id); tk.State != core.TaskQueued {
		t.Fatalf("state %s", tk.State)
	}
	if items := inboxFor(t, e, id); len(items) != 1 {
		t.Fatalf("inbox items for the task = %d, want 1", len(items))
	}
}

func TestHandleUpdateIsRetrySafe(t *testing.T) {
	ctx := context.Background()
	e, flaky := d2FlakyTasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id, err := e.tasks.Create(ctx, "claude@proj", "/w/proj", "gpu-box", "work", nil)
	if err != nil {
		t.Fatal(err)
	}

	running := d2Env(t, peer, core.KindTaskUpdate, "codex@train", "claude@proj", core.TaskUpdateBody{TaskID: id, State: core.TaskRunning, Note: "halfway"})
	handleRetry(t, flaky, func() error { return e.tasks.HandleUpdate(ctx, peer, running) })
	if tk := e.state(t, id); tk.State != core.TaskRunning || len(tk.Notes) != 1 {
		t.Fatalf("after running update: %s notes %+v", tk.State, tk.Notes)
	}

	done := d2Env(t, peer, core.KindTaskUpdate, "codex@train", "claude@proj", core.TaskUpdateBody{TaskID: id, State: core.TaskDone, Result: "42"})
	handleRetry(t, flaky, func() error { return e.tasks.HandleUpdate(ctx, peer, done) })
	if tk := e.state(t, id); tk.State != core.TaskDone || tk.Result != "42" {
		t.Fatalf("after done update: %+v", tk)
	}
	items := inboxFor(t, e, id)
	if len(items) != 2 || items[0].Item.MsgID != running.ID || items[1].Item.MsgID != done.ID {
		t.Fatalf("inbox = %+v, want the running and done updates once each", items)
	}
}

func TestHandleCancelIsRetrySafe(t *testing.T) {
	ctx := context.Background()
	e, flaky := d2FlakyTasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id := e.incoming(t, peer, "", "work")
	if _, err := e.tasks.Claim(ctx, "claude@proj", id); err != nil {
		t.Fatal(err)
	}
	before := len(inboxFor(t, e, id))
	cancel := d2Env(t, peer, core.KindTaskCancel, "codex@train", "", core.TaskCancelBody{TaskID: id})
	handleRetry(t, flaky, func() error { return e.tasks.HandleCancel(ctx, peer, cancel) })
	if tk := e.state(t, id); tk.State != core.TaskCancelled || len(tk.Notes) != 1 {
		t.Fatalf("after cancel: %s notes %+v", tk.State, tk.Notes)
	}
	if n := len(inboxFor(t, e, id)) - before; n != 1 {
		t.Fatalf("cancel notices = %d, want 1", n)
	}
}
