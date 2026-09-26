package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// fManagedPorts records what the managed-session methods asked for.
type fManagedPorts struct {
	lw       *linkWorld
	mu       sync.Mutex
	calls    []string
	released chan string
}

func (f *fManagedPorts) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fManagedPorts) List(_ context.Context, machine string) ([]ipc.OfferView, error) {
	f.record("offers.list " + machine)
	return []ipc.OfferView{{Machine: "mac", Label: "trainer", Folder: "/srv/proj", Permission: "tasks-auto", RunMode: "shell"}}, nil
}

func (f *fManagedPorts) Set(_ context.Context, p ipc.OfferSetParams, unlocked bool) (ipc.OfferView, error) {
	if !unlocked {
		return ipc.OfferView{}, core.ErrAuthRequired
	}
	f.record("offers.set " + p.Machine + " " + p.Label + " " + p.RunMode)
	return ipc.OfferView{Machine: p.Machine, Label: p.Label, Folder: p.Folder, Permission: p.Permission, RunMode: p.RunMode}, nil
}

func (f *fManagedPorts) Remove(_ context.Context, machine, label string, unlocked bool) error {
	if !unlocked {
		return core.ErrAuthRequired
	}
	f.record("offers.remove " + machine + " " + label)
	return nil
}

type fManaged struct{ *fManagedPorts }

func (f fManaged) List(context.Context) ([]ipc.ManagedView, error) {
	return []ipc.ManagedView{{Name: "trainer-ab12", Machine: "mac", State: "idle"}}, nil
}

func (f fManaged) Open(_ context.Context, name string) (ipc.ManagedOpenResult, func(), error) {
	if name != "trainer-ab12" {
		return ipc.ManagedOpenResult{}, nil, core.ErrNotFound
	}
	f.record("open " + name)
	return ipc.ManagedOpenResult{Name: name, Folder: "/srv/proj", Command: []string{"claude", "--resume", "u-1"}},
		func() { f.released <- name }, nil
}

func (f fManaged) Close(_ context.Context, name string) error {
	f.record("close " + name)
	return nil
}

// Bind knows one token, for session S-run.
func (f *fManagedPorts) Bind(_ context.Context, token string, conn uint64) (RunBinding, error) {
	if token != "run-token" {
		return RunBinding{}, core.ErrNotFound
	}
	f.lw.mu.Lock()
	f.lw.bound["S-run"] = conn
	f.lw.mu.Unlock()
	return RunBinding{ID: "S-run", Folder: "/srv/managed", View: ipc.SharedSessionView{Name: "trainer-ab12", Kind: "managed", State: "open"}}, nil
}

func managedServer(t *testing.T) (*world, *fManagedPorts, func() *ipc.Client) {
	t.Helper()
	w := newWorld()
	f := &fManagedPorts{lw: w.lw, released: make(chan string, 2)}
	srv := NewServer(w.ports(), core.NewFakeClock(time.Unix(1_700_000_000, 0)), nil)
	RegisterManaged(srv, ManagedPorts{Offers: f, Managed: fManaged{f}, Runs: f})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return w, f, func() *ipc.Client {
		c, _ := srv.Pipe(ctx)
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func register(t *testing.T, c *ipc.Client) {
	t.Helper()
	if err := c.Call(bg, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: "/srv/proj"}, nil); err != nil {
		t.Fatal(err)
	}
}

// A run token binds the connection to the run's session, and from then on
// the connection reaches only that session's messages, tasks, files
// and link.
func TestRunBindScopesTheConnection(t *testing.T) {
	w, _, dial := managedServer(t)
	c := dial()
	if err := c.Call(bg, ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: "run-token"}, nil); !errors.Is(err, core.ErrNoSession) {
		t.Fatalf("binding before registering: %v", err)
	}
	register(t, c)
	if err := c.Call(bg, ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: "guess"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("a wrong token: %v", err)
	}
	var v ipc.SharedSessionView
	if err := c.Call(bg, ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: "run-token"}, &v); err != nil || v.Name != "trainer-ab12" {
		t.Fatalf("bind: %+v, %v", v, err)
	}
	var r ipc.InboxResult
	if err := c.Call(bg, ipc.MethodInboxCheck, ipc.InboxCheckParams{Limit: 5}, &r); !errors.Is(err, core.ErrNotPermitted) {
		t.Fatalf("a run reading its inbox (the host's queue): %v", err)
	}
	if w.lastSession != "" {
		t.Fatalf("an inbox was read for %q", w.lastSession)
	}
	for _, m := range []struct {
		method string
		params any
	}{
		{ipc.MethodSessionShare, ipc.SessionShareParams{Name: "escape"}},
		{ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "mac/lead", Permission: "tasks-auto"}},
		{ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: 1, Accept: true}},
		{ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}},
		{ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "mac", Label: "x", Folder: "/srv", Permission: "tasks-auto"}},
		{ipc.MethodManagedClose, ipc.ManagedNameParams{Name: "trainer-ab12"}},
		{ipc.MethodKill, nil},
		{ipc.MethodPeerPause, ipc.AliasParams{Alias: "mac"}},
		{ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: 1, Instructions: "x"}},
	} {
		if err := c.Call(bg, m.method, m.params, nil); !errors.Is(err, core.ErrNotPermitted) {
			t.Errorf("%s from a run: %v, want not_permitted", m.method, err)
		}
	}
	// A chat that shares a session cannot turn into a run.
	chat := dial()
	register(t, chat)
	if err := chat.Call(bg, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "lead"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := chat.Call(bg, ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: "run-token"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("a sharing chat binding a run: %v", err)
	}
}

func TestOfferMethodsNeedTheOwnerAndThePassword(t *testing.T) {
	_, f, dial := managedServer(t)
	c := dial()
	set := ipc.OfferSetParams{Machine: "mac", Label: "trainer", Folder: "/srv/proj", Permission: "tasks-auto", RunMode: "shell", ShellConfirm: "shell"}
	if err := c.Call(bg, ipc.MethodOffersSet, set, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("set without the password: %v", err)
	}
	if err := c.Call(bg, ipc.MethodOffersRemove, ipc.OfferRemoveParams{Machine: "mac", Label: "trainer"}, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("remove without the password: %v", err)
	}
	var list ipc.OffersListResult
	if err := c.Call(bg, ipc.MethodOffersList, ipc.OffersListParams{Machine: "mac"}, &list); err != nil || len(list.Offers) != 1 {
		t.Fatalf("list: %+v, %v", list, err)
	}
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, nil); err != nil {
		t.Fatal(err)
	}
	bad := set
	bad.Folder = "relative/path"
	if err := c.Call(bg, ipc.MethodOffersSet, bad, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("a relative folder: %v", err)
	}
	var v ipc.OfferView
	if err := c.Call(bg, ipc.MethodOffersSet, set, &v); err != nil || v.Label != "trainer" {
		t.Fatalf("set: %+v, %v", v, err)
	}
	if err := c.Call(bg, ipc.MethodOffersRemove, ipc.OfferRemoveParams{Machine: "mac", Label: "trainer"}, nil); err != nil {
		t.Fatal(err)
	}
	// An agent connection is refused even with the password.
	agent := dial()
	register(t, agent)
	if err := agent.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		method string
		params any
	}{
		{ipc.MethodOffersSet, set},
		{ipc.MethodOffersList, nil},
		{ipc.MethodOffersRemove, ipc.OfferRemoveParams{Machine: "mac", Label: "trainer"}},
		{ipc.MethodManagedList, nil},
		{ipc.MethodManagedOpen, ipc.ManagedNameParams{Name: "trainer-ab12"}},
		{ipc.MethodManagedClose, ipc.ManagedNameParams{Name: "trainer-ab12"}},
	} {
		if err := agent.Call(bg, m.method, m.params, nil); !errors.Is(err, ipc.ErrBadRequest) {
			t.Errorf("%s from an agent: %v, want bad_request", m.method, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 3 || f.calls[1] != "offers.set mac trainer shell" || f.calls[2] != "offers.remove mac trainer" {
		t.Fatalf("calls %q", f.calls)
	}
}

// managed.open holds the queue until the connection that opened it ends.
func TestManagedOpenHoldsUntilTheConnectionEnds(t *testing.T) {
	_, f, dial := managedServer(t)
	c := dial()
	var r ipc.ManagedOpenResult
	if err := c.Call(bg, ipc.MethodManagedOpen, ipc.ManagedNameParams{Name: "trainer-ab12"}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Folder != "/srv/proj" || len(r.Command) != 3 || r.Command[0] != "claude" {
		t.Fatalf("open %+v", r)
	}
	var list ipc.ManagedListResult
	if err := c.Call(bg, ipc.MethodManagedList, nil, &list); err != nil || len(list.Sessions) != 1 {
		t.Fatalf("list %+v, %v", list, err)
	}
	select {
	case <-f.released:
		t.Fatal("released while the connection is open")
	case <-time.After(100 * time.Millisecond):
	}
	c.Close()
	select {
	case name := <-f.released:
		if name != "trainer-ab12" {
			t.Fatalf("released %q", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hold outlived the connection")
	}
	if err := dial().Call(bg, ipc.MethodManagedOpen, ipc.ManagedNameParams{Name: "nope"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	// Closing a managed session is a cut-off: it works while killed.
	k := dial()
	if err := k.Call(bg, ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := k.Call(bg, ipc.MethodManagedClose, ipc.ManagedNameParams{Name: "trainer-ab12"}, nil); err != nil {
		t.Fatalf("close while killed: %v", err)
	}
}

// A run's connection sends files from its managed session's folder, not
// from the folder its process registered with.
func TestRunBoundFilesUseTheManagedFolder(t *testing.T) {
	w, _, dial := managedServer(t)
	c := dial()
	if err := c.Call(bg, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: "/"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(bg, ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: "run-token"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(bg, ipc.MethodFileSend, ipc.FileSendParams{Link: 4, Path: "out/model.bin"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := w.lastCall(); got != "file S-run 4 /srv/managed out/model.bin" {
		t.Fatalf("file sent as %q", got)
	}
}
