package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/store/sqlite"
)

// hookEnv is one machine with two chats sharing sessions from one folder.
type hookEnv struct {
	st     *sqlite.DB
	clock  *core.FakeClock
	shared *SessionService
	inbox  *InboxService
	att    *AttentionService
	hooks  *HookService
	peer   store.Peer
}

func newHookEnv(t *testing.T) *hookEnv {
	t.Helper()
	e := &hookEnv{st: d2Store(t), clock: core.NewFakeClock(d2Epoch)}
	e.shared, e.inbox = d2Inbox(t, e.st, e.clock)
	e.att = attentionOn(e.st, e.shared, e.inbox)
	e.hooks = NewHookService(e.shared, e.att)
	e.peer, _ = d2Peer(t, e.st, "gpu-box")
	return e
}

// share shares a session called name from /w/proj, with an active link.
func (e *hookEnv) share(t *testing.T, name string) (store.SharedSession, store.Link) {
	t.Helper()
	sh := e.shareWake(t, name)
	return sh.Session, d2Link(t, e.st, e.peer, sh.Session, "trainer-"+name, core.PermTasksAuto, core.PermTasksAuto)
}

func (e *hookEnv) shareWake(t *testing.T, name string) Shared {
	t.Helper()
	e.clock.Advance(1)
	sh, err := e.shared.Share(context.Background(), d2Conn.Add(1), ShareRequest{Agent: "claude", ProjectDir: "/w/proj", Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

// deliver puts one item of kind on the session's link.
func (e *hookEnv) deliver(t *testing.T, s store.SharedSession, l store.Link, kind core.Kind) {
	t.Helper()
	body, _ := json.Marshal(core.ChatBody{Text: "hello"})
	if _, err := e.inbox.Deliver(context.Background(), store.InboxItem{MsgID: core.NewID(), From: e.peer.MachineID, ToSession: s.ID, LinkID: l.ID, Kind: kind, Body: body}); err != nil {
		t.Fatal(err)
	}
}

// Review focus: the Stop hook blocks only for unhandled items, never for
// decisions alone, respects stop_hook_active (no second block for the same
// items) and stops after MaxStopBlocks blocks in a row.
func TestStopHookDecisions(t *testing.T) {
	ctx := context.Background()
	e := newHookEnv(t)
	s, l := e.share(t, "lead")
	e.hooks.Bind("chat-1", s.ID)
	stop := func(active bool) HookAnswer {
		t.Helper()
		a, err := e.hooks.Check(ctx, HookQuery{AgentSession: "chat-1", Cwd: "/w/proj", Event: HookStop, StopHookActive: active})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if a := stop(false); a.Block {
		t.Fatalf("nothing pending blocked: %+v", a)
	}
	e.deliver(t, s, l, KindApprovalNotice)
	e.deliver(t, s, l, core.KindLinkRequest)
	if a := stop(false); a.Block || a.Counts.Unread != 2 {
		t.Fatalf("decisions alone blocked: %+v", a)
	}
	e.deliver(t, s, l, core.KindChat)
	a := stop(false)
	if !a.Block || !strings.HasPrefix(a.Reason, "cravv-connect: 1 new message on link") || !strings.HasSuffix(a.Reason, "Then start the listener again.") ||
		strings.Contains(a.Reason, "\n") {
		t.Fatalf("unhandled chat: %+v", a)
	}
	if a := stop(true); a.Block {
		t.Fatal("stop_hook_active with the same items blocked again")
	}
	e.deliver(t, s, l, core.KindTaskUpdate)
	if a := stop(false); !a.Block {
		t.Fatal("a fresh stop with unhandled items did not block")
	}
	e.deliver(t, s, l, core.KindChat)
	if a := stop(true); !a.Block {
		t.Fatal("new items while continuing did not block (2nd in a row)")
	}
	e.deliver(t, s, l, core.KindChat)
	if a := stop(true); a.Block {
		t.Fatalf("a 3rd block in a row")
	}
	if _, err := e.inbox.Check(ctx, s.ID, 50); err != nil {
		t.Fatal(err)
	}
	if a := stop(false); a.Block {
		t.Fatal("blocked after check_inbox read everything")
	}
}

// Hook identity (v2 spec 3.2): two chats in one folder are told apart by
// the chat ID the MCP server recorded; without one, the newest session of
// the folder whose chat is unknown answers.
func TestHookFindsTheChatsOwnSession(t *testing.T) {
	ctx := context.Background()
	e := newHookEnv(t)
	one, l1 := e.share(t, "one")
	two, l2 := e.share(t, "two")
	e.hooks.Bind("chat-1", one.ID)
	e.hooks.Bind("chat-2", two.ID)
	e.deliver(t, one, l1, core.KindChat)
	check := func(chat string) HookAnswer {
		t.Helper()
		a, err := e.hooks.Check(ctx, HookQuery{AgentSession: chat, Cwd: "/w/proj", Event: HookUserPromptSubmit})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if a := check("chat-1"); !strings.Contains(a.Notice, "on link "+itoa(l1.Num)) {
		t.Fatalf("chat-1: %+v", a)
	}
	if a := check("chat-2"); strings.Contains(a.Notice, "new message") {
		t.Fatalf("chat-2 saw chat-1's item: %+v", a)
	}
	if a := check("chat-unknown"); a.Notice != "" {
		t.Fatalf("an unknown chat matched a bound session: %+v", a)
	}
	three, l3 := e.share(t, "three") // shared by an agent that gives no chat ID
	e.deliver(t, three, l3, core.KindTaskCreate)
	if a := check(""); !strings.Contains(a.Notice, "1 new task on link "+itoa(l3.Num)) {
		t.Fatalf("fallback by folder: %+v", a)
	}
	e.deliver(t, two, l2, core.KindChat)
	e.hooks.Bind("chat-3", two.ID) // a reattach from a new chat takes the binding
	if a := check("chat-3"); !strings.Contains(a.Notice, "on link "+itoa(l2.Num)) {
		t.Fatalf("the new chat: %+v", a)
	}
	if a := check("chat-2"); strings.Contains(a.Notice, "on link "+itoa(l2.Num)) {
		t.Fatalf("the old chat still maps: %+v", a)
	}
	if err := e.shared.Close(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	if a := check("chat-1"); a.Notice != "" || a.Block {
		t.Fatalf("a closed session answered: %+v", a)
	}
}

// UserPromptSubmit reminds about decisions the agent already read and a
// listener that is not running.
func TestPromptNoticeReminders(t *testing.T) {
	ctx := context.Background()
	e := newHookEnv(t)
	sh := e.shareWake(t, "lead")
	s := sh.Session
	e.hooks.Bind("chat-1", s.ID)
	q := HookQuery{AgentSession: "chat-1", Cwd: "/w/proj", Event: HookUserPromptSubmit}
	a, err := e.hooks.Check(ctx, q)
	if err != nil || a.Notice != "cravv-connect: The listener for this chat's session is not running: start it again as a background command." {
		t.Fatalf("no listener: %q, %v", a.Notice, err)
	}
	if _, err := e.st.InsertLink(ctx, store.Link{Peer: e.peer.MachineID, ID: core.NewID(), Direction: store.LinkInbound, Session: s.ID,
		RemoteSession: core.NewID(), RemoteName: "x", Proposed: core.PermMessages, State: store.LinkPending, CreatedAt: d2Epoch, UpdatedAt: d2Epoch}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	lctx, cancel := context.WithCancel(ctx)
	go func() { e.att.Listen(lctx, sh.WakeToken, 0); close(done) }()
	for !e.att.Listening(s.ID) {
		time.Sleep(time.Millisecond)
	}
	a, _ = e.hooks.Check(ctx, q)
	if a.Notice != "cravv-connect: 1 decision waits for your human. Call review_pending." {
		t.Fatalf("pending decision: %q", a.Notice)
	}
	cancel()
	<-done
	if e.att.Listening(s.ID) {
		t.Fatal("still counted as listening")
	}
}

// Review focus: a session found only by folder (no chat ID bound) may
// belong to another chat in that folder, so its Stop hook never keeps a
// chat going; it only gives the notice.
func TestStopHookNeverBlocksOnFolderFallback(t *testing.T) {
	ctx := context.Background()
	e := newHookEnv(t)
	s, l := e.share(t, "lead") // shared by an agent that gives no chat ID
	e.deliver(t, s, l, core.KindChat)
	for _, chat := range []string{"", "chat-unbound"} {
		a, err := e.hooks.Check(ctx, HookQuery{AgentSession: chat, Cwd: "/w/proj", Event: HookStop})
		if err != nil {
			t.Fatal(err)
		}
		if a.Block || a.Reason != "" || !strings.Contains(a.Notice, "1 new message on link "+itoa(l.Num)) {
			t.Fatalf("chat %q: %+v", chat, a)
		}
	}
	e.hooks.Bind("chat-1", s.ID)
	if a, _ := e.hooks.Check(ctx, HookQuery{AgentSession: "chat-1", Cwd: "/w/proj", Event: HookStop}); !a.Block {
		t.Fatalf("the bound chat: %+v", a)
	}
}
