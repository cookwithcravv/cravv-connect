package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// A server started by a managed run binds every connection with its run
// token and offers only the run tools.
func TestRunTokenBindsTheRunsSession(t *testing.T) {
	d := newDaemonFake(t)
	var tokens []string
	d.handle(ipc.MethodSessionRunBind, ipc.GateSession, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.RunBindParams
		_ = json.Unmarshal(raw, &p)
		d.mu.Lock()
		tokens = append(tokens, p.RunToken)
		d.mu.Unlock()
		if p.RunToken != "tok-1" {
			return nil, ipc.ErrBadRequest
		}
		cs.SetShared("S-run")
		return ipc.SharedSessionView{Name: "trainer-ab12"}, nil
	})
	d.handle(ipc.MethodLinks, ipc.GateShared, func(cs *ipc.ConnState, _ json.RawMessage) (any, error) {
		return ipc.LinksResult{Links: []ipc.LinkView{{Link: 4, Session: cs.Shared()}}}, nil
	})
	d.start()
	cs, _ := connectWith(t, d, "claude-code", Options{RunToken: "tok-1"}, nil, "")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	// No inbox tools: the host owns the queue and gives the run its item
	// in the prompt; reading the inbox would take items from the queue.
	want := []string{"links", "send_message", "get_task", "claim_task", "update_task", "complete_task", "fail_task", "send_file"}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("a run's tools %q, want %q", names, want)
	}
	if init := cs.InitializeResult(); !strings.Contains(init.Instructions, "No human is at this machine") {
		t.Fatalf("instructions %q", init.Instructions)
	}
	text, isErr := callTool(t, cs, "links", nil)
	if isErr || !strings.Contains(text, "S-run") {
		t.Fatalf("links: %q (error %v)", text, isErr)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !slices.Equal(tokens, []string{"tok-1"}) || len(d.regs) != 1 {
		t.Fatalf("tokens %q, registrations %d", tokens, len(d.regs))
	}
}

// With a wrong token nothing reaches the daemon as the session.
func TestWrongRunTokenActsAsNobody(t *testing.T) {
	d := newDaemonFake(t)
	d.handle(ipc.MethodSessionRunBind, ipc.GateSession, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return nil, ipc.ErrBadRequest
	})
	called := false
	d.handle(ipc.MethodLinks, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) {
		called = true
		return ipc.LinksResult{}, nil
	})
	d.start()
	cs, _ := connectWith(t, d, "claude-code", Options{RunToken: "stale"}, nil, "")
	if _, isErr := callTool(t, cs, "links", nil); !isErr || called {
		t.Fatalf("links with a stale token: error %v, daemon called %v", isErr, called)
	}
}
