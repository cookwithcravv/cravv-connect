package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

func TestShareBindsTheConnection(t *testing.T) {
	h := newHarness(t)
	lw := h.w.lw
	plain := h.dial(t)
	if err := plain.Call(bg, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "lead"}, nil); !errors.Is(err, core.ErrNoSession) {
		t.Fatalf("share before session.register: %v", err)
	}
	c := h.session(t)
	if err := c.Call(bg, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "gpu-box/trainer", Permission: "messages"}, nil); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("connect before sharing: %v", err)
	}
	var res ipc.ShareResult
	if err := c.Call(bg, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "lead", Visibility: "all-peers"}, &res); err != nil {
		t.Fatal(err)
	}
	if res.WakeToken != "wake-S1" || res.ReattachToken != "reattach-S1" || res.Session.Name != "lead" {
		t.Fatalf("share result %+v", res)
	}
	if got := lw.last(); got != "share claude /work/proj lead all-peers" {
		t.Fatalf("share used %q: agent and folder must come from the registration", got)
	}
	if err := c.Call(bg, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "again"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("second share on one connection: %v", err)
	}
	if err := c.Call(bg, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "gpu-box/trainer", Permission: "tasks-ask", Note: "hi"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := lw.last(); got != "connect S1 gpu-box/trainer tasks-ask hi" {
		t.Fatalf("connect call %q", got)
	}

	// A second connection takes the session over with the reattach token.
	c2 := h.session(t)
	if err := c2.Call(bg, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: "wrong"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("bad token: %v", err)
	}
	if err := c2.Call(bg, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: res.ReattachToken}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(bg, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "gpu-box/trainer", Permission: "messages"}, nil); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("the replaced connection still acts as the session: %v", err)
	}
	if err := c.Call(bg, ipc.MethodLinks, nil, nil); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("the replaced connection must not fall back to every link: %v", err)
	}
	if err := c2.Call(bg, ipc.MethodLinks, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := lw.last(); got != `links "S1"` {
		t.Fatalf("links scope %q", got)
	}
	c2.Close()
	select {
	case id := <-lw.detached:
		if id != "S1" {
			t.Fatalf("detached %q", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing the connection did not detach the shared session")
	}
}

func TestLinkMethodsScopeAndGates(t *testing.T) {
	h := newHarness(t)
	lw := h.w.lw
	human := h.dial(t)
	if err := human.Call(bg, ipc.MethodLinks, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := lw.last(); got != `links ""` {
		t.Fatalf("an unshared connection sees every link: %q", got)
	}
	if err := human.Call(bg, ipc.MethodLinkDisconnect, ipc.LinkParams{}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("missing link number: %v", err)
	}
	if err := human.Call(bg, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: 3, Permission: "messages"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := lw.last(); got != `restrict "" 3 messages` {
		t.Fatalf("restrict %q", got)
	}
	if err := human.Call(bg, ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: 3, Permission: "tasks-auto"}, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("permit without the password: %v", err)
	}
	if err := human.Call(bg, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: 3, Accept: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := lw.last(); got != "decide 3 true  false" {
		t.Fatalf("decide before unlock %q", got)
	}
	unlock(t, human)
	if err := human.Call(bg, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: 3, Accept: true, Permission: "tasks-auto"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := lw.last(); got != "decide 3 true tasks-auto true" {
		t.Fatalf("decide after unlock %q", got)
	}
	var sl ipc.SessionsListResult
	if err := human.Call(bg, ipc.MethodSessionsList, ipc.MachineParams{Machine: "gpu-box"}, &sl); err != nil || len(sl.Sessions) != 1 {
		t.Fatalf("sessions.list %+v, %v", sl, err)
	}
	var lr ipc.ListenResult
	if err := human.Call(bg, ipc.MethodSessionListen, ipc.SessionListenParams{WakeToken: "w", TimeoutS: 5}, &lr); err != nil || lr.Unread != 2 || lr.Requests != 1 {
		t.Fatalf("listen %+v, %v", lr, err)
	}
	if got := lw.last(); got != "listen w 5s" {
		t.Fatalf("listen call %q", got)
	}
}

// An agent connection that has not shared a session (or closed the one it
// shared) acts for no session, so it must not fall back to every link the
// way a human CLI connection does.
func TestUnsharedAgentGetsNoLinks(t *testing.T) {
	h := newHarness(t)
	lw := h.w.lw
	check := func(c *ipc.Client, when string) {
		t.Helper()
		if err := c.Call(bg, ipc.MethodLinks, nil, nil); !errors.Is(err, core.ErrNotShared) {
			t.Fatalf("links %s: %v", when, err)
		}
		if err := c.Call(bg, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: 3}, nil); !errors.Is(err, core.ErrNotShared) {
			t.Fatalf("disconnect %s: %v", when, err)
		}
		if err := c.Call(bg, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: 3, Permission: "messages"}, nil); !errors.Is(err, core.ErrNotShared) {
			t.Fatalf("restrict %s: %v", when, err)
		}
	}
	agent := h.session(t)
	check(agent, "before sharing")

	c := h.shared(t)
	if err := c.Call(bg, ipc.MethodSessionClose, nil, nil); err != nil {
		t.Fatal(err)
	}
	check(c, "after session_close")
	lw.mu.Lock()
	defer lw.mu.Unlock()
	for _, call := range lw.calls {
		if strings.HasPrefix(call, "links") || strings.HasPrefix(call, "disconnect") || strings.HasPrefix(call, "restrict") {
			t.Fatalf("an unshared agent reached the link service: %q", call)
		}
	}
}
