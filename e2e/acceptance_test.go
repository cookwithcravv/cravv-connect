package e2e

// The v2 acceptance suite: one test per success criterion of spec section 2
// (docs/superpowers/specs/2026-09-26-cravv-connect-v2-sessions-design.md),
// each against real daemons on an in-process relay, through the surfaces a
// person and an agent use (the MCP server, the listener process, the CLI,
// the web UI). The criterion each test pins is quoted above it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// shareChat shares an MCP chat and returns the listener command it got.
func shareChat(t *testing.T, m *mcpAgent, name, visibility string) string {
	t.Helper()
	var out struct {
		Listener  string `json:"listener"`
		WakeToken string `json:"wake_token"`
	}
	m.decode("session_share", map[string]any{"name": name, "purpose": name + " work", "visibility": visibility}, &out)
	if out.Listener == "" || out.WakeToken != "" {
		t.Fatalf("session_share returned %+v", out)
	}
	return out.Listener
}

// taskState polls a task on c until it reaches state.
func taskState(t *testing.T, c *ipc.Client, id, state string) ipc.TaskView {
	t.Helper()
	var tv ipc.TaskView
	Eventually(t, wait, "task "+id+" "+state, func() bool {
		Call(t, c, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: id}, &tv)
		return tv.State == state
	})
	return tv
}

// TestAcceptance_1_ChatIsTheHub pins criterion 1, "The chat is the hub":
// "Everything addressed to a session shows up in that session's chat
// without the human asking, including while the chat is idle." and
// "Accepting and rejecting happens in the chat for everything except the
// few actions that need a password (section 10)."
//
// The idle chat is a real `cravv-connect listen` process started from the
// command session_share returned, as Claude Code runs it in the
// background; its exit is what wakes the chat.
func TestAcceptance_1_ChatIsTheHub(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	human := &Human{}
	trainer, _ := newClaudeAgent(t, gpu, "chat-trainer", human)
	lead, _ := newClaudeAgent(t, mac, "chat-lead", nil)
	listener := shareChat(t, trainer, "trainer", "all-peers")
	shareChat(t, lead, "lead", "private")

	// A link request wakes the idle chat, which decides it in a form.
	idle := gpu.ListenerProcess(t, listener)
	Silent(t, idle, 300*time.Millisecond, "nothing addressed to the session yet")
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-ask", "note": "ACC1-NOTE"}, &out)
	in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	r := Waited(t, idle, wait, "the link request wakes the chat")
	if want := fmt.Sprintf("cravv-connect: 1 link request on link %d from alice. Call check_inbox, then review_pending.\n", in.Link); r.Code != 0 || r.Stdout != want {
		t.Fatalf("listener %+v, want %q", r, want)
	}
	trainer.call("check_inbox", nil)
	human.Answer(Choose("accept"))
	if text := trainer.call("review_pending", nil); !strings.Contains(text, "accepted") {
		t.Fatalf("review_pending %q", text)
	}
	mac.WaitLink(wait, "accepted in the chat", func(l ipc.LinkView) bool {
		return l.Link == out.Link && l.State == "active" && l.PermissionOut == "tasks-ask"
	})

	// A message wakes it again; the chat reads it wrapped.
	idle = gpu.ListenerProcess(t, listener)
	Silent(t, idle, 300*time.Millisecond, "everything read")
	lead.call("send_message", map[string]any{"link": out.Link, "text": "ACC1-HELLO"})
	r = Waited(t, idle, wait, "the message wakes the chat")
	if want := fmt.Sprintf("cravv-connect: 1 new message on link %d from alice. Call check_inbox.\n", in.Link); r.Stdout != want {
		t.Fatalf("listener %q, want %q", r.Stdout, want)
	}
	if text := trainer.call("check_inbox", nil); !strings.Contains(text, "<remote_message") || !strings.Contains(text, "ACC1-HELLO") {
		t.Fatalf("check_inbox %q", text)
	}

	// A task that needs approval wakes it; the human rejects it in the chat.
	idle = gpu.ListenerProcess(t, listener)
	var task ipc.TaskCreateResult
	lead.decode("create_task", map[string]any{"link": out.Link, "instructions": "ACC1-TASK wipe the cache"}, &task)
	r = Waited(t, idle, wait, "the task wakes the chat")
	if !strings.Contains(r.Stdout, fmt.Sprintf("1 task awaiting approval on link %d from alice", in.Link)) {
		t.Fatalf("listener %q", r.Stdout)
	}
	trainer.call("check_inbox", nil)
	human.Answer(Choose("reject"))
	if text := trainer.call("review_pending", nil); !strings.Contains(text, "denied") {
		t.Fatalf("review_pending %q", text)
	}
	Eventually(t, wait, "the sender sees the rejection", func() bool {
		return strings.Contains(lead.call("get_task", map[string]any{"task_id": task.TaskID}), `"state": "rejected"`)
	})

	// Without the listener (an agent that did not re-arm it) the prompt
	// hook still names what arrived and asks for the listener again, so
	// delivery degrades to the next turn.
	lead.call("send_message", map[string]any{"link": out.Link, "text": "ACC1-AGAIN"})
	arrived := fmt.Sprintf("cravv-connect: 1 new message on link %d from alice.", in.Link)
	var notice string
	Eventually(t, wait, "the prompt notice", func() bool {
		notice = gpu.Hook("UserPromptSubmit", "chat-trainer", false)
		return strings.HasPrefix(notice, arrived)
	})
	if !strings.Contains(notice, "The listener for this chat's session is not running") || strings.Contains(notice, "ACC1-AGAIN") {
		t.Fatalf("prompt notice %q", notice)
	}
	trainer.call("check_inbox", nil)

	// Agents without the listener poll with wait_for_message.
	trainer.call("send_message", map[string]any{"link": in.Link, "text": "ACC1-REPLY"})
	Eventually(t, wait, "wait_for_message returns the reply", func() bool {
		return strings.Contains(lead.call("wait_for_message", map[string]any{"timeout_s": 5}), "ACC1-REPLY")
	})

	// Granting tasks-auto is one of the password actions: the chat's form
	// never offers it and says where the password goes.
	second, _ := newClaudeAgent(t, mac, "chat-second", nil)
	shareChat(t, second, "second", "private")
	idle = gpu.ListenerProcess(t, listener)
	var auto ipc.LinkView
	second.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-auto"}, &auto)
	Waited(t, idle, wait, "the tasks-auto request wakes the chat")
	trainer.call("check_inbox", nil)
	human.Answer(Choose("accept as tasks-ask"))
	trainer.call("review_pending", nil)
	forms := human.Forms()
	form := forms[len(forms)-1]
	if !strings.Contains(form.Message, "Granting tasks-auto needs your password: run cravv-connect link accept") {
		t.Fatalf("form %q", form.Message)
	}
	for _, c := range formChoices(t, form) {
		if strings.Contains(c, "tasks-auto") {
			t.Fatalf("the chat's form offers %q", c)
		}
	}
	granted := mac.WaitLink(wait, "accepted lower", func(l ipc.LinkView) bool { return l.Link == auto.Link && l.State == "active" })
	if granted.PermissionOut != "tasks-ask" {
		t.Fatalf("the chat granted %q", granted.PermissionOut)
	}
	raise := gpu.WaitLink(wait, "the second link", func(l ipc.LinkView) bool { return l.RemoteSession == "second" && l.State == "active" })
	wantKind(t, TryCall(gpu.Conn(), ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: raise.Link, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
	Call(t, gpu.Unlocked(), ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: raise.Link, Permission: "tasks-auto"}, nil)
	mac.WaitLink(wait, "raised with the password", func(l ipc.LinkView) bool { return l.Link == auto.Link && l.PermissionOut == "tasks-auto" })
}

// TestAcceptance_2_SessionLinks pins criterion 2, "Session-to-session
// links": "Two sessions can only exchange messages, tasks or files over an
// accepted link.", "A session may hold several links." and "Traffic on one
// link is never visible to another session."
//
// Two sessions per machine: mac/lead and mac/notes, gpu/trainer and
// gpu/helper. lead links to both GPU sessions; notes links to none.
func TestAcceptance_2_SessionLinks(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	lead := mac.Share("claude", "lead", "private")
	notes := mac.Share("codex", "notes", "private")
	trainer := gpu.Share("claude", "trainer", "all-peers")
	helper := gpu.Share("codex", "helper", "all-peers")
	if err := os.WriteFile(filepath.Join(mac.Proj, "acc2.txt"), []byte("ACC2-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before the link is accepted nothing can be sent on it.
	pending := Connect(t, lead, "bob/trainer", "tasks-auto", "")
	wantKind(t, TryCall(lead.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: pending.Link, Text: "too early"}, nil), ipc.KindLinkClosed)
	wantKind(t, TryCall(lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: pending.Link, Instructions: "too early"}, nil), ipc.KindLinkClosed)
	wantKind(t, TryCall(lead.C, ipc.MethodFileSend, ipc.FileSendParams{Link: pending.Link, Path: "acc2.txt"}, nil), ipc.KindLinkClosed)
	// Pairing alone links nothing: the machine's other session has no link.
	wantKind(t, TryCall(notes.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: pending.Link, Text: "not mine"}, nil), ipc.KindNotFound)
	in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Session == "trainer" })
	gpu.Decide(in.Link, true, "")
	mac.WaitLink(wait, "link one active", func(l ipc.LinkView) bool { return l.Link == pending.Link && l.State == "active" })
	one := Linked{A: lead, B: trainer, ANum: pending.Link, BNum: in.Link}
	two := LinkChats(t, mac, gpu, lead, helper, "tasks-auto")

	var mine ipc.LinksResult
	Call(t, lead.C, ipc.MethodLinks, nil, &mine)
	if len(mine.Links) != 2 || mine.Links[0].State != "active" || mine.Links[1].State != "active" {
		t.Fatalf("lead holds %+v, want two active links", mine.Links)
	}
	for _, s := range []*SharedChat{lead, trainer, helper} {
		Inbox(t, s.C) // the link notices
	}

	// Traffic on link one: a message, a task and a file.
	msg := sendChat(t, lead.C, one.ANum, "ACC2-CHAT-ONE")
	var task ipc.TaskCreateResult
	Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: one.ANum, Instructions: "ACC2-TASK-ONE"}, &task)
	var file ipc.FileSendResult
	Call(t, lead.C, ipc.MethodFileSend, ipc.FileSendParams{Link: one.ANum, Path: "acc2.txt"}, &file)
	var got []ipc.InboxView
	Eventually(t, wait, "link one's traffic at trainer", func() bool {
		got = append(got, Inbox(t, trainer.C)...)
		var chat, tsk, f bool
		for _, it := range got {
			chat = chat || it.ID == msg
			tsk = tsk || it.TaskID == task.TaskID
			f = f || (it.FileID == file.FileID && it.Path != "")
		}
		return chat && tsk && f
	})
	// ... and on link two, only helper's.
	msg2 := sendChat(t, lead.C, two.ANum, "ACC2-CHAT-TWO")
	WaitItem(t, helper.C, wait, "link two's message at helper", isChat(msg2))
	for _, it := range got {
		if strings.Contains(it.Wrapped, "ACC2-CHAT-TWO") {
			t.Fatalf("trainer saw link two's message: %+v", it)
		}
	}
	for _, it := range Inbox(t, helper.C) {
		if strings.Contains(it.Wrapped, "ACC2-") && !strings.Contains(it.Wrapped, "ACC2-CHAT-TWO") {
			t.Fatalf("helper saw link one's traffic: %+v", it)
		}
	}
	if items := Inbox(t, notes.C); len(items) != 0 {
		t.Fatalf("notes, with no link, saw %+v", items)
	}

	// Cross-talk is impossible, not just hidden: other sessions cannot
	// reach link one's task or send on its number, on either machine.
	for name, c := range map[string]*ipc.Client{"helper": helper.C, "notes": notes.C} {
		wantKind(t, TryCall(c, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: task.TaskID}, nil), ipc.KindNotFound)
		wantKind(t, TryCall(c, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: task.TaskID}, nil), ipc.KindNotFound)
		if err := TryCall(c, ipc.MethodTaskCancel, ipc.TaskIDParams{TaskID: task.TaskID}, nil); !ipc.IsKind(err, ipc.KindNotFound) {
			t.Fatalf("%s cancelled link one's task: %v", name, err)
		}
	}
	wantKind(t, TryCall(helper.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: one.BNum, Text: "hijack"}, nil), ipc.KindNotFound)
	wantKind(t, TryCall(notes.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: one.ANum, Text: "hijack"}, nil), ipc.KindNotFound)
	Call(t, trainer.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: task.TaskID}, nil)
	Call(t, trainer.C, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: task.TaskID, Result: "ACC2-RESULT"}, nil)
	taskState(t, lead.C, task.TaskID, "done")
	for _, it := range Inbox(t, notes.C) {
		t.Fatalf("notes saw the task's updates: %+v", it)
	}
}

// TestAcceptance_3_Presence pins criterion 3, "Presence": "When a linked
// session closes, the other side learns within 5 seconds (both machines
// online), or within 150 seconds when a machine drops." and "A session
// that is only briefly away (sleep, MCP reconnect) does not lose its
// links."
func TestAcceptance_3_Presence(t *testing.T) {
	t.Parallel()

	t.Run("a closed session reaches the peer within 5 seconds", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		l := LinkUp(t, mac, gpu, "messages")
		start := time.Now()
		Call(t, l.B.C, ipc.MethodSessionClose, nil, nil)
		mac.WaitLink(5*time.Second, "session_closed", func(v ipc.LinkView) bool {
			return v.Link == l.ANum && v.State == "closed" && v.Reason == core.CloseSessionClosed
		})
		t.Logf("the Mac learned of the close after %s", time.Since(start).Round(time.Millisecond))
		WaitItem(t, l.A.C, 5*time.Second, "the Mac's chat is told", func(it ipc.InboxView) bool {
			return it.Link == l.ANum && strings.Contains(it.Wrapped, "closed")
		})
	})

	t.Run("a machine that drops closes its links within 150 seconds", func(t *testing.T) {
		t.Parallel()
		clock := core.NewFakeClock(time.Now())
		r, mac, gpu := NewPairWithClock(t, clock)
		l := LinkUp(t, mac, gpu, "messages")
		ctx := context.Background()
		tick := func() {
			for _, n := range []*Node{mac, gpu} {
				if err := n.Daemon.Presence().Tick(ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		// The relay goes first, so no presence frame from before the drop
		// can arrive late; the links' timeouts start at the first tick.
		r.Stop()
		tick()
		for range 5 { // 150 seconds of pings nobody receives
			clock.Advance(core.PresenceInterval)
			tick()
		}
		if a, b := mac.Link(l.ANum), gpu.Link(l.BNum); a.State != "active" || b.State != "active" {
			t.Fatalf("closed before 150 seconds: %+v %+v", a, b)
		}
		clock.Advance(time.Second)
		tick()
		for _, v := range []ipc.LinkView{mac.Link(l.ANum), gpu.Link(l.BNum)} {
			if v.State != "closed" || v.Reason != core.ClosePresenceTimeout {
				t.Fatalf("after 150 seconds: %+v", v)
			}
		}
	})

	t.Run("a briefly away session keeps its links", func(t *testing.T) {
		t.Parallel()
		clock := core.NewFakeClock(time.Now())
		r, mac, gpu := NewPairWithClock(t, clock)
		l := LinkUp(t, mac, gpu, "messages")
		ctx := context.Background()
		tick := func(n *Node) {
			if err := n.Daemon.Presence().Tick(ctx); err != nil {
				t.Fatal(err)
			}
		}

		// MCP reconnect: the chat's connection drops, the session is away,
		// what is sent meanwhile waits, and a reattach brings it all back.
		l.B.C.Close()
		mac.WaitLink(wait, "the peer is away", func(v ipc.LinkView) bool { return v.Link == l.ANum && v.RemoteAway && v.State == "active" })
		queued := sendChat(t, l.A.C, l.ANum, "ACC3-WHILE-AWAY")
		back := gpu.Reattach("claude", l.B)
		mac.WaitLink(wait, "the peer is back", func(v ipc.LinkView) bool { return v.Link == l.ANum && !v.RemoteAway && v.State == "active" })
		WaitItem(t, back.C, wait, "what queued while away", isChat(queued))

		// Sleep: both machines lose the relay for less than the timeout.
		r.Stop()
		tick(mac)
		tick(gpu)
		clock.Advance(4 * core.PresenceInterval) // 120 seconds
		tick(mac)
		tick(gpu)
		r.Start()
		mac.WaitOnline()
		gpu.WaitOnline()
		// Presence resumes: the Mac's ping reaches the GPU box before the
		// chat sent after it, and the GPU box's pong reaches the Mac before
		// the reply sent after that (one mailbox, in order).
		tick(mac)
		ping := sendChat(t, l.A.C, l.ANum, "ACC3-AWAKE")
		WaitItem(t, back.C, wait, "the chat after the ping", isChat(ping))
		tick(gpu)
		pong := sendChat(t, back.C, l.BNum, "ACC3-AWAKE-TOO")
		WaitItem(t, l.A.C, wait, "the reply after the pong", isChat(pong))
		clock.Advance(4 * core.PresenceInterval) // 240 seconds since the drop
		tick(mac)
		tick(gpu)
		if a, b := mac.Link(l.ANum), gpu.Link(l.BNum); a.State != "active" || b.State != "active" {
			t.Fatalf("a brief absence closed the link: %+v %+v", a, b)
		}
	})
}
