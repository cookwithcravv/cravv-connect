package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

var sampleLinks = ipc.LinksResult{Links: []ipc.LinkView{
	{Link: 1, Machine: "gpu-box", Session: "lead", RemoteSession: "trainer", Direction: "out", State: "active", PermissionIn: "messages", PermissionOut: "tasks-auto"},
	{Link: 2, Machine: "mac", Session: "lead", RemoteSession: "helper", Direction: "in", State: "pending", Proposed: "tasks-ask",
		Wrapped: "<remote_message from=\"mac\">\nnote: please\x1b[2J\n</remote_message>"},
	{Link: 3, Machine: "gpu-box", Session: "lead", RemoteSession: "old", Direction: "out", State: "closed", Reason: "presence_timeout", PermissionIn: "messages"},
}}

func TestLinksTable(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodLinks, ipc.GateNone, sampleLinks)
	fd.start()
	r := fd.run(nil, "links")
	want := "" +
		"LINK  SESSION  PEER             STATE                     THEY MAY        YOU MAY\n" +
		"1     lead     gpu-box/trainer  active                    messages        tasks-auto\n" +
		"2     lead     mac/helper       request (you decide)      asks tasks-ask  -\n" +
		"3     lead     gpu-box/old      closed: presence_timeout  messages        -\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("code %d\n%s\nwant\n%s", r.code, r.stdout, want)
	}
}

// A link whose peer machine stopped answering presence is away.
func TestLinkStateUnreachable(t *testing.T) {
	if got := linkState(ipc.LinkView{State: "active", RemoteAway: true, Unreachable: true}); got != "away (peer machine not answering)" {
		t.Fatalf("state %q", got)
	}
}

func TestLinksEmpty(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodLinks, ipc.GateNone, ipc.LinksResult{})
	fd.start()
	if r := fd.run(nil, "links"); !strings.HasPrefix(r.stdout, "No links yet.") {
		t.Fatalf("%q", r.stdout)
	}
}

// Accepting asks for the password only because the daemon requires it, and
// shows the request (peer text only as a prefixed block) before deciding.
func TestLinkAcceptAsksForThePassword(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodLinks, ipc.GateNone, sampleLinks)
	fd.handle(ipc.MethodLinkDecide, ipc.GateNone, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		if !cs.Unlocked() {
			return nil, core.ErrAuthRequired
		}
		return ipc.LinkView{Link: 2, Machine: "mac", RemoteSession: "helper", State: "active", PermissionIn: "messages"}, nil
	})
	fd.start()
	p := &fakePrompter{passwords: []string{"pw"}}
	r := fd.run(p, "link", "accept", "2", "--permission", "messages")
	if r.code != 0 {
		t.Fatalf("code %d %q %q", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{
		"Link 2: mac/helper asks to link with your session lead, with permission tasks-ask.\n",
		"| note: please[2J\n",
		"Accepted link 2: mac/helper may now use messages.\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "\x1b") {
		t.Fatal("an escape sequence from the peer reached the terminal")
	}
	if got := fd.params(ipc.MethodLinkDecide); got != `{"link":2,"accept":true,"permission":"messages"}` {
		t.Fatalf("decide params %s", got)
	}
	if len(p.asked) != 1 {
		t.Fatalf("password asked %d times", len(p.asked))
	}
}

func TestLinkRejectNeedsNoPassword(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodLinkDecide, ipc.GateNone, ipc.LinkView{Link: 2, State: "closed"})
	fd.start()
	p := &fakePrompter{}
	r := fd.run(p, "link", "reject", "2")
	if r.code != 0 || r.stdout != "Rejected link 2.\n" || len(p.asked) != 0 {
		t.Fatalf("code %d %q asked %v", r.code, r.stdout, p.asked)
	}
	if got := fd.params(ipc.MethodLinkDecide); got != `{"link":2,"accept":false}` {
		t.Fatalf("params %s", got)
	}
	if r := fd.run(p, "link", "reject", "two"); r.code != 1 || !strings.Contains(r.stderr, "not a link number") {
		t.Fatalf("bad number: %d %q", r.code, r.stderr)
	}
}

func TestSessionsTable(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodSessionsList, ipc.GateNone, ipc.SessionsListResult{Machine: "gpu-box", Sessions: []ipc.RemoteSessionView{
		{Name: "trainer", Kind: "live", Agent: "claude", State: "open"},
		{Name: "eval", Kind: "live", State: "away"},
	}})
	fd.start()
	r := fd.run(nil, "sessions", "gpu-box")
	want := "" +
		"SESSION          STATE  KIND  AGENT\n" +
		"gpu-box/trainer  open   live  claude\n" +
		"gpu-box/eval     away   live  -\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("code %d\n%s\nwant\n%s", r.code, r.stdout, want)
	}
	if got := fd.params(ipc.MethodSessionsList); got != `{"machine":"gpu-box"}` {
		t.Fatalf("params %s", got)
	}
}

// Raising: the ungated link.restrict refuses it (as the daemon does), so
// permit asks for the password and calls link.permit.
func TestLinkPermitAsksForThePassword(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodLinkRestrict, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return nil, core.ErrAuthRequired
	})
	fd.handle(ipc.MethodLinkPermit, ipc.GateUnlock, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		return ipc.LinkView{Link: 1, PermissionIn: "tasks-auto"}, nil
	})
	fd.start()
	p := &fakePrompter{passwords: []string{"pw"}}
	r := fd.run(p, "link", "permit", "1", "tasks-auto")
	if r.code != 0 || r.stdout != "Link 1 now allows tasks-auto.\n" || len(p.asked) != 1 {
		t.Fatalf("code %d %q asked %v", r.code, r.stdout, p.asked)
	}
	if got := fd.params(ipc.MethodLinkPermit); got != `{"link":1,"permission":"tasks-auto"}` {
		t.Fatalf("params %s", got)
	}
}

// Lowering what a link allows needs no password: permit tries the ungated
// link.restrict first and never calls the gated link.permit.
func TestLinkPermitLoweringNeedsNoPassword(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodLinkRestrict, ipc.GateNone, ipc.LinkView{Link: 1, PermissionIn: "messages"})
	fd.handle(ipc.MethodLinkPermit, ipc.GateUnlock, func(*ipc.ConnState, json.RawMessage) (any, error) {
		t.Error("lowering called link.permit")
		return nil, nil
	})
	fd.start()
	p := &fakePrompter{}
	r := fd.run(p, "link", "permit", "1", "messages")
	if r.code != 0 || r.stdout != "Link 1 now allows messages.\n" || len(p.asked) != 0 {
		t.Fatalf("code %d %q %q asked %v", r.code, r.stdout, r.stderr, p.asked)
	}
	if got := fd.params(ipc.MethodLinkRestrict); got != `{"link":1,"permission":"messages"}` {
		t.Fatalf("params %s", got)
	}
}

func TestLinkDisconnectNeedsNoPassword(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodLinkDisconnect, ipc.GateNone, nil)
	fd.start()
	p := &fakePrompter{}
	r := fd.run(p, "link", "disconnect", "3")
	if r.code != 0 || r.stdout != "Disconnected link 3.\n" || len(p.asked) != 0 {
		t.Fatalf("code %d %q %q asked %v", r.code, r.stdout, r.stderr, p.asked)
	}
	if got := fd.params(ipc.MethodLinkDisconnect); got != `{"link":3}` {
		t.Fatalf("params %s", got)
	}
	if r := fd.run(p, "link", "disconnect", "x"); r.code != 1 || !strings.Contains(r.stderr, "not a link number") {
		t.Fatalf("bad number: %d %q", r.code, r.stderr)
	}
}

func TestLinkRestrictNeedsNoPassword(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodLinkRestrict, ipc.GateNone, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.LinkPermissionParams
		json.Unmarshal(raw, &p)
		if p.Link == 2 {
			return nil, core.ErrAuthRequired // link 2 allows messages: tasks-ask would raise it
		}
		return ipc.LinkView{Link: p.Link, PermissionIn: p.Permission}, nil
	})
	fd.start()
	p := &fakePrompter{}
	r := fd.run(p, "link", "restrict", "1", "tasks-ask")
	if r.code != 0 || r.stdout != "Link 1 now allows tasks-ask.\n" || len(p.asked) != 0 {
		t.Fatalf("code %d %q %q asked %v", r.code, r.stdout, r.stderr, p.asked)
	}
	if got := fd.params(ipc.MethodLinkRestrict); got != `{"link":1,"permission":"tasks-ask"}` {
		t.Fatalf("params %s", got)
	}
	// Raising is permit's job; restrict says so and never asks.
	r = fd.run(p, "link", "restrict", "2", "tasks-ask")
	if r.code != 1 || !strings.Contains(r.stderr, "cravv-connect link permit 2 tasks-ask") || len(p.asked) != 0 {
		t.Fatalf("raise: code %d %q asked %v", r.code, r.stderr, p.asked)
	}
	// tasks-auto is the top level: restricting to it can never lower.
	before := len(fd.methods())
	r = fd.run(p, "link", "restrict", "1", "tasks-auto")
	if r.code != 1 || !strings.Contains(r.stderr, "messages or tasks-ask") || len(fd.methods()) != before {
		t.Fatalf("tasks-auto: code %d %q calls %v", r.code, r.stderr, fd.methods())
	}
	if r := fd.run(p, "link", "restrict", "0", "messages"); r.code != 1 || !strings.Contains(r.stderr, "not a link number") {
		t.Fatalf("bad number: %d %q", r.code, r.stderr)
	}
}
