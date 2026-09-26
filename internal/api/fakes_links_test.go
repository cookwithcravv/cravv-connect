package api

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// linkWorld backs the fake shared-session, discovery and link ports.
type linkWorld struct {
	mu       sync.Mutex
	bound    map[string]uint64 // session ID -> connection ID
	nextID   int
	calls    []string
	detached chan string
}

func newLinkWorld() *linkWorld {
	return &linkWorld{bound: map[string]uint64{}, detached: make(chan string, 4)}
}

func (w *linkWorld) record(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, fmt.Sprintf(format, args...))
}

func (w *linkWorld) last() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.calls) == 0 {
		return ""
	}
	return w.calls[len(w.calls)-1]
}

type fShared struct{ *linkWorld }

func (f fShared) Share(_ context.Context, conn uint64, agent, dir, name, purpose, vis string) (string, ipc.ShareResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("S%d", f.nextID)
	f.bound[id] = conn
	f.calls = append(f.calls, fmt.Sprintf("share %s %s %s %s", agent, dir, name, vis))
	return id, ipc.ShareResult{Session: ipc.SharedSessionView{Name: name, Purpose: purpose, State: "open"},
		WakeToken: "wake-" + id, ReattachToken: "reattach-" + id}, nil
}

func (f fShared) Current(_ context.Context, id string, conn uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.bound[id]; !ok || c != conn {
		return core.ErrNotShared
	}
	return nil
}

func (f fShared) Close(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bound, id)
	f.calls = append(f.calls, "close "+id)
	return nil
}

func (f fShared) Set(_ context.Context, id string, purpose, vis *string) (ipc.SharedSessionView, error) {
	v := ipc.SharedSessionView{Name: id}
	if purpose != nil {
		v.Purpose = *purpose
	}
	if vis != nil {
		v.Visibility = *vis
	}
	f.record("set %s", id)
	return v, nil
}

func (f fShared) Reattach(_ context.Context, conn uint64, token, agent, dir string) (string, ipc.SharedSessionView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.bound {
		if token == "reattach-"+id && agent == "claude" && dir == "/work/proj" {
			f.bound[id] = conn
			return id, ipc.SharedSessionView{Name: id, State: "open"}, nil
		}
	}
	return "", ipc.SharedSessionView{}, core.ErrNotFound
}

func (f fShared) Detach(_ context.Context, id string, conn uint64) error {
	f.detached <- id
	return nil
}

func (f fShared) Listen(_ context.Context, token string, timeout time.Duration) (ipc.ListenResult, error) {
	f.record("listen %s %s", token, timeout)
	return ipc.ListenResult{Unread: 2, Requests: 1}, nil
}

type fDiscovery struct{ *linkWorld }

func (f fDiscovery) Sessions(_ context.Context, machine string) (ipc.SessionsListResult, error) {
	f.record("sessions %s", machine)
	return ipc.SessionsListResult{Machine: machine, Sessions: []ipc.RemoteSessionView{{Name: "trainer", Kind: "live", State: "open"}}}, nil
}

type fLinks struct{ *linkWorld }

func (f fLinks) Connect(_ context.Context, sessionID, target, perm, note string) (ipc.LinkView, error) {
	f.record("connect %s %s %s %s", sessionID, target, perm, note)
	return ipc.LinkView{Link: 1, State: "pending"}, nil
}

func (f fLinks) List(_ context.Context, sessionID string) ([]ipc.LinkView, error) {
	f.record("links %q", sessionID)
	return nil, nil
}

func (f fLinks) Disconnect(_ context.Context, sessionID string, link int64) error {
	f.record("disconnect %q %d", sessionID, link)
	return nil
}

func (f fLinks) Restrict(_ context.Context, sessionID string, link int64, perm string) (ipc.LinkView, error) {
	f.record("restrict %q %d %s", sessionID, link, perm)
	return ipc.LinkView{Link: link}, nil
}

func (f fLinks) Permit(_ context.Context, link int64, perm string, unlocked bool) (ipc.LinkView, error) {
	f.record("permit %d %s %v", link, perm, unlocked)
	return ipc.LinkView{Link: link}, nil
}

func (f fLinks) Decide(_ context.Context, sessionID string, link int64, accept bool, perm string, unlocked bool) (ipc.LinkView, error) {
	f.record("decide %q %d %v %s %v", sessionID, link, accept, perm, unlocked)
	return ipc.LinkView{Link: link}, nil
}
