package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
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
	return d2FlakyTasksWith(t, core.PermTasksAuto)
}

// d2FlakyTasksWith is d2FlakyTasks where gpu-box may do perm on the link.
func d2FlakyTasksWith(t *testing.T, perm core.Permission) (*d2TaskEnv, *flakyInbox) {
	t.Helper()
	e := d2Tasks(t, perm)
	flaky := &flakyInbox{InboxStore: e.st}
	e.inbox = NewInboxService(flaky, e.shared, e.st, e.st, e.clock)
	e.tasks = NewTaskService(TaskDeps{
		Tasks: e.st, Peers: e.st, Links: e.links, Lookup: e.st, Inbox: e.inbox, Sender: e.sender,
		Files: e.files, Desktop: e.desktop, Clock: e.clock, Audit: e.audit,
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

// inboxFor returns the session's inbox items for a task, read or not.
func inboxFor(t *testing.T, e *d2TaskEnv, taskID string) []store.InboxItem {
	t.Helper()
	all, err := e.st.SessionItems(context.Background(), e.session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.InboxItem
	for _, it := range all {
		if it.TaskID == taskID {
			out = append(out, it)
		}
	}
	return out
}

func TestHandleCreateRedeliversAfterRetryableFailure(t *testing.T) {
	ctx := context.Background()
	e, flaky := d2FlakyTasks(t)
	id := core.NewID()
	env := d2Env(t, e.peer, core.KindTaskCreate, e.link.ID, core.TaskCreateBody{TaskID: id, Instructions: "work"})
	handleRetry(t, flaky, func() error { return e.tasks.HandleCreate(withLink(ctx, e.link), e.peer, env) })
	if tk := e.state(t, id); tk.State != core.TaskQueued {
		t.Fatalf("state %s", tk.State)
	}
	if items := inboxFor(t, e, id); len(items) != 1 {
		t.Fatalf("inbox items for the task = %d, want 1", len(items))
	}
}

// A held task whose approval notice was not stored (a retryable failure),
// and which a human then approved, is delivered once: the relay's
// redelivery of the task.create must not deliver it a second time.
func TestApprovedTaskIsNotDeliveredAgainOnRedelivery(t *testing.T) {
	ctx := context.Background()
	e, flaky := d2FlakyTasksWith(t, core.PermTasksAsk)
	id := core.NewID()
	env := d2Env(t, e.peer, core.KindTaskCreate, e.link.ID, core.TaskCreateBody{TaskID: id, Instructions: "work"})
	flaky.failNext(1)
	var re *RetryableError
	if err := e.tasks.HandleCreate(withLink(ctx, e.link), e.peer, env); !errors.As(err, &re) {
		t.Fatalf("first attempt err = %v, want retryable", err)
	}
	if tk := e.state(t, id); tk.State != core.TaskAwaitingApproval {
		t.Fatalf("state %s, want awaiting_approval", tk.State)
	}
	if err := e.tasks.Decide(ctx, id, true, AuthChat); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := e.tasks.HandleCreate(withLink(ctx, e.link), e.peer, env); err != nil {
			t.Fatalf("redelivery: %v", err)
		}
	}
	n := 0
	for _, it := range inboxFor(t, e, id) {
		if it.Kind == core.KindTaskCreate {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the approved task was delivered %d times, want once", n)
	}
}

func TestHandleUpdateIsRetrySafe(t *testing.T) {
	ctx := context.Background()
	e, flaky := d2FlakyTasks(t)
	id, err := e.tasks.Create(ctx, e.session.ID, "/w/proj", e.link.Num, "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	lctx := withLink(ctx, e.link)
	running := d2Env(t, e.peer, core.KindTaskUpdate, e.link.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskRunning, Note: "halfway"})
	handleRetry(t, flaky, func() error { return e.tasks.HandleUpdate(lctx, e.peer, running) })
	if tk := e.state(t, id); tk.State != core.TaskRunning || len(tk.Notes) != 1 {
		t.Fatalf("after running update: %s notes %+v", tk.State, tk.Notes)
	}

	done := d2Env(t, e.peer, core.KindTaskUpdate, e.link.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskDone, Result: "42"})
	handleRetry(t, flaky, func() error { return e.tasks.HandleUpdate(lctx, e.peer, done) })
	if tk := e.state(t, id); tk.State != core.TaskDone || tk.Result != "42" {
		t.Fatalf("after done update: %+v", tk)
	}
	items := inboxFor(t, e, id)
	if len(items) != 2 || items[0].MsgID != running.ID || items[1].MsgID != done.ID {
		t.Fatalf("inbox = %+v, want the running and done updates once each", items)
	}
}

func TestHandleCancelIsRetrySafe(t *testing.T) {
	ctx := context.Background()
	e, flaky := d2FlakyTasks(t)
	id := e.incoming(t, "work")
	if _, err := e.tasks.Claim(ctx, e.session.ID, id); err != nil {
		t.Fatal(err)
	}
	before := len(inboxFor(t, e, id))
	cancel := d2Env(t, e.peer, core.KindTaskCancel, e.link.ID, core.TaskCancelBody{TaskID: id})
	handleRetry(t, flaky, func() error { return e.tasks.HandleCancel(withLink(ctx, e.link), e.peer, cancel) })
	if tk := e.state(t, id); tk.State != core.TaskCancelled || len(tk.Notes) != 1 {
		t.Fatalf("after cancel: %s notes %+v", tk.State, tk.Notes)
	}
	if n := len(inboxFor(t, e, id)) - before; n != 1 {
		t.Fatalf("cancel notices = %d, want 1", n)
	}
}
