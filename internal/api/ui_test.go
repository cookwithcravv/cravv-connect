package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

type fUI struct{ calls int }

func (f *fUI) Start(context.Context) (string, error) {
	f.calls++
	return fmt.Sprintf("http://127.0.0.1:4000/launch?token=t%d", f.calls), nil
}

type fLocal struct{ closed *[]string }

func (fLocal) Local(context.Context) ([]ipc.SharedSessionView, error) {
	return []ipc.SharedSessionView{{Name: "lead", State: "open", Visibility: "all-peers"}, {Name: "nap", State: "away"}}, nil
}

func (f fLocal) CloseByName(_ context.Context, name string) (ipc.SharedSessionView, error) {
	if name != "lead" && name != "nap" {
		return ipc.SharedSessionView{}, core.ErrNotFound
	}
	if f.closed != nil {
		*f.closed = append(*f.closed, name)
	}
	return ipc.SharedSessionView{Name: name, Kind: "live", State: "closed"}, nil
}

func (fLocal) OpenByName(_ context.Context, name string) (string, error) {
	if name == "lead" {
		return "S-lead", nil
	}
	return "", core.ErrNotFound
}

// uiServer serves the real method groups plus RegisterUI over in-process
// pipes.
func uiServer(t *testing.T) (*world, *fUI, func() *ipc.Client) {
	t.Helper()
	w := newWorld()
	ui := &fUI{}
	srv := NewServer(w.ports(), core.NewFakeClock(time.Unix(1_700_000_000, 0)), nil)
	RegisterUI(srv, UIPorts{UI: ui, Local: fLocal{}, Links: fLinks{w.lw}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return w, ui, func() *ipc.Client {
		c, _ := srv.Pipe(ctx)
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func TestUIStartReturnsLaunchURL(t *testing.T) {
	_, ui, dial := uiServer(t)
	var r ipc.UIStartResult
	if err := dial().Call(bg, ipc.MethodUIStart, nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.URL != "http://127.0.0.1:4000/launch?token=t1" {
		t.Fatalf("url %q", r.URL)
	}
	// It works while killed, so the human can resume from the page.
	if err := dial().Call(bg, ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := dial().Call(bg, ipc.MethodUIStart, nil, &r); err != nil || ui.calls != 2 {
		t.Fatalf("while killed: %v (calls %d)", err, ui.calls)
	}
}

// Agent connections cannot start the UI, list every local session or ask
// for links on another session's behalf.
func TestUIMethodsRefuseAgentConnections(t *testing.T) {
	_, ui, dial := uiServer(t)
	c := dial()
	if err := c.Call(bg, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: "/work/proj"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{ipc.MethodUIStart, ipc.MethodSessionsLocal, ipc.MethodSessionsClose} {
		if err := c.Call(bg, m, nil, nil); !errors.Is(err, ipc.ErrBadRequest) {
			t.Errorf("%s from an agent: %v", m, err)
		}
	}
	err := c.Call(bg, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "lead", Target: "gpu-box/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, ipc.ErrBadRequest) {
		t.Errorf("link.connect_as from an agent: %v", err)
	}
	if ui.calls != 0 {
		t.Fatalf("UI started %d times", ui.calls)
	}
}

func TestSessionsLocalListsViews(t *testing.T) {
	_, _, dial := uiServer(t)
	var r ipc.LocalSessionsResult
	if err := dial().Call(bg, ipc.MethodSessionsLocal, nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 2 || r.Sessions[0].Name != "lead" || r.Sessions[1].State != "away" {
		t.Fatalf("sessions %+v", r.Sessions)
	}
}

// link.connect_as needs the password and an open local session by name,
// then connects exactly like the session's own link.connect.
func TestLinkConnectAsNeedsPasswordAndOpenSession(t *testing.T) {
	w, _, dial := uiServer(t)
	c := dial()
	p := ipc.LinkConnectAsParams{Session: "lead", Target: "gpu-box/trainer", Permission: "tasks-ask", Note: "hi"}
	if err := c.Call(bg, ipc.MethodLinkConnectAs, p, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("without password: %v", err)
	}
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, nil); err != nil {
		t.Fatal(err)
	}
	var v ipc.LinkView
	if err := c.Call(bg, ipc.MethodLinkConnectAs, p, &v); err != nil {
		t.Fatal(err)
	}
	if got := w.lw.last(); got != "connect S-lead gpu-box/trainer tasks-ask hi" || v.State != "pending" {
		t.Fatalf("connect call %q, view %+v", got, v)
	}
	if err := c.Call(bg, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "nap", Target: "gpu-box/trainer", Permission: "messages"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("away session: %v", err)
	}
	if err := c.Call(bg, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "lead", Permission: "messages"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("missing target: %v", err)
	}
}

// sessions.close closes a local session by name for the human (CLI or web
// UI): no password, and it works while killed (a cut-off).
func TestSessionsCloseByName(t *testing.T) {
	var closed []string
	srv := NewServer(newWorld().ports(), core.NewFakeClock(time.Unix(1_700_000_000, 0)), nil)
	RegisterUI(srv, UIPorts{UI: &fUI{}, Local: fLocal{closed: &closed}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c, _ := srv.Pipe(ctx)
	t.Cleanup(func() { c.Close() })
	if gate := srv.Methods()[ipc.MethodSessionsClose]; gate != ipc.GateAllowWhenKilled {
		t.Fatalf("gate %b", gate)
	}
	if err := c.Call(bg, ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	var v ipc.SharedSessionView
	if err := c.Call(bg, ipc.MethodSessionsClose, ipc.SessionNameParams{Name: "lead"}, &v); err != nil || v.Name != "lead" || v.State != "closed" {
		t.Fatalf("close: %+v, %v", v, err)
	}
	if err := c.Call(bg, ipc.MethodSessionsClose, ipc.SessionNameParams{Name: "ghost"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if err := c.Call(bg, ipc.MethodSessionsClose, ipc.SessionNameParams{}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("no name: %v", err)
	}
	if len(closed) != 1 || closed[0] != "lead" {
		t.Fatalf("closed %v", closed)
	}
}
