package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/store/sqlite"
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

// d2Resolver resolves "alias" or "alias/session" straight from the peer store.
type d2Resolver struct{ peers store.PeerStore }

func (r d2Resolver) Resolve(ctx context.Context, addr string) (store.Peer, string, error) {
	alias, session, _ := strings.Cut(addr, "/")
	p, err := r.peers.GetPeerByAlias(ctx, alias)
	return p, session, err
}

// d2Files is a FileSender that records calls.
type d2Files struct {
	mu    sync.Mutex
	calls []string // "taskID:path"
}

func (f *d2Files) SendFile(ctx context.Context, to core.MachineID, projectDir, path, taskID string) (core.FileRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, taskID+":"+path)
	return core.FileRef{FileID: core.NewID(), Name: filepath.Base(path), Size: 42}, nil
}

type d2TaskEnv struct {
	st      *sqlite.DB
	clock   *core.FakeClock
	reg     *SessionRegistry
	inbox   *InboxService
	sender  *d2Sender
	desktop *d2Desktop
	audit   *d2Audit
	files   *d2Files
	tasks   *TaskService
}

func d2Tasks(t *testing.T) *d2TaskEnv {
	t.Helper()
	e := &d2TaskEnv{st: d2Store(t), clock: core.NewFakeClock(d2Epoch), sender: &d2Sender{}, desktop: &d2Desktop{}, audit: &d2Audit{}, files: &d2Files{}}
	e.reg, e.inbox = d2Inbox(t, e.st, e.clock)
	e.tasks = NewTaskService(TaskDeps{
		Tasks: e.st, Peers: e.st, Resolver: d2Resolver{e.st}, Inbox: e.inbox, Sender: e.sender,
		Policy: TrustPolicy{}, Files: e.files, Desktop: e.desktop, Clock: e.clock, Audit: e.audit,
	})
	return e
}

// incoming delivers a task.create from peer and returns the task ID.
func (e *d2TaskEnv) incoming(t *testing.T, peer store.Peer, toSession, instructions string) string {
	t.Helper()
	id := core.NewID()
	env := d2Env(t, peer, core.KindTaskCreate, "codex@train", toSession, core.TaskCreateBody{TaskID: id, Instructions: instructions})
	if err := e.tasks.HandleCreate(context.Background(), peer, env); err != nil {
		t.Fatalf("HandleCreate: %v", err)
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
