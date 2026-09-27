package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/store/sqlite"
)

// d2Audit records audit events.
type d2Audit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *d2Audit) Record(e audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *d2Audit) ofType(typ string) []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audit.Event
	for _, e := range a.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// d2Files is a FileSender that records calls.
type d2Files struct {
	mu    sync.Mutex
	calls []string // "taskID:path"
	links []string // link IDs used
}

func (f *d2Files) SendFile(ctx context.Context, l store.Link, projectDir, path, taskID string) (core.FileRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, taskID+":"+path)
	f.links = append(f.links, l.ID)
	return core.FileRef{FileID: core.NewID(), Name: filepath.Base(path), Size: 42}, nil
}

// d2TaskEnv is one machine with a TaskService, the shared session "lead"
// and an active link to session "trainer" on peer gpu-box.
type d2TaskEnv struct {
	st      *sqlite.DB
	clock   *core.FakeClock
	shared  *SessionService
	inbox   *InboxService
	links   *LinkService
	sender  *d2Sender
	desktop *d2Desktop
	audit   *d2Audit
	files   *d2Files
	replies *d2Replies
	tasks   *TaskService

	peer    store.Peer
	session store.SharedSession
	link    store.Link
}

// d2Tasks builds the environment; perm is what gpu-box may do on the link.
func d2Tasks(t *testing.T, perm core.Permission) *d2TaskEnv {
	t.Helper()
	e := &d2TaskEnv{st: d2Store(t), clock: core.NewFakeClock(d2Epoch), sender: &d2Sender{}, desktop: &d2Desktop{},
		audit: &d2Audit{}, files: &d2Files{}, replies: &d2Replies{}}
	e.shared, e.inbox = d2Inbox(t, e.st, e.clock)
	e.links = NewLinkService(LinkDeps{Links: e.st, Sessions: e.shared, Peers: e.st, Sender: e.sender,
		Replies: e.replies, Inbox: e.inbox, Clock: e.clock})
	e.tasks = NewTaskService(TaskDeps{
		Tasks: e.st, Peers: e.st, Links: e.links, Lookup: e.st, Inbox: e.inbox, Sender: e.sender,
		Files: e.files, Desktop: e.desktop, Clock: e.clock, Audit: e.audit,
	})
	e.links.AddCloseObserver(e.tasks)
	e.links.AddLowerObserver(e.tasks)
	e.peer, _ = d2Peer(t, e.st, "gpu-box")
	e.session = d2Share(t, e.shared, "lead")
	e.link = d2Link(t, e.st, e.peer, e.session, "trainer", perm, core.PermTasksAuto)
	return e
}

// handle runs env through the LinkGate and the task handler for its kind,
// as the machine env.FromMachine (which must be a stored peer).
func (e *d2TaskEnv) handle(t *testing.T, env core.Envelope) error {
	t.Helper()
	peer, err := e.st.GetPeer(context.Background(), env.FromMachine)
	if err != nil {
		t.Fatalf("sender %s: %v", env.FromMachine, err)
	}
	var inner, onReject Handler
	switch env.Kind {
	case core.KindTaskCreate:
		inner, onReject = HandlerFunc(e.tasks.HandleCreate), HandlerFunc(e.tasks.RejectCreate)
	case core.KindTaskUpdate:
		inner = HandlerFunc(e.tasks.HandleUpdate)
	case core.KindTaskCancel:
		inner = HandlerFunc(e.tasks.HandleCancel)
	default:
		t.Fatalf("no task handler for %s", env.Kind)
	}
	return d2Gated(e.st, e.shared, e.replies, inner, onReject).Handle(context.Background(), peer, env)
}

// incoming delivers a task.create on the link and returns the task ID.
func (e *d2TaskEnv) incoming(t *testing.T, instructions string) string {
	t.Helper()
	id := core.NewID()
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCreate, e.link.ID, core.TaskCreateBody{TaskID: id, Instructions: instructions})); err != nil {
		t.Fatalf("task.create: %v", err)
	}
	return id
}

func (e *d2TaskEnv) state(t *testing.T, id string) store.Task {
	t.Helper()
	tk, err := e.st.GetTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}
