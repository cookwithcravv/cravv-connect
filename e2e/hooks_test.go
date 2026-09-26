package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// ShareAs is Share for an agent chat whose own ID (Claude Code's session
// ID) the MCP server passes on, so the chat's hooks find the session.
func (n *Node) ShareAs(agent, name, visibility, chatID string) *SharedChat {
	n.t.Helper()
	c, _ := n.Session(agent)
	var res ipc.ShareResult
	Call(n.t, c, ipc.MethodSessionShare, ipc.SessionShareParams{Name: name, Purpose: name + " work", Visibility: visibility, AgentSession: chatID}, &res)
	return &SharedChat{C: c, Name: name, Res: res}
}

// Hook runs `cravv-connect hook` with Claude Code's hook JSON for chatID.
func (n *Node) Hook(event, chatID string, stopHookActive bool) string {
	n.t.Helper()
	in, _ := json.Marshal(map[string]any{"session_id": chatID, "cwd": n.Proj, "hook_event_name": event, "stop_hook_active": stopHookActive})
	r := n.RunCLI(string(in), "hook")
	if r.Code != 0 || r.Stderr != "" {
		n.t.Fatalf("hook: %+v", r)
	}
	return r.Stdout
}

// v2 spec 7.1 and 3.2: the Stop hook of the chat whose session has an
// unhandled item blocks once with a one-line reason; the other chat in the
// same folder is not disturbed; stop_hook_active and reading the inbox end it.
func TestStopHookPerChat(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	lead := a.Share("claude", "lead", "private")
	trainer := b.ShareAs("claude", "trainer", "all-peers", "chat-trainer")
	b.ShareAs("claude", "helper", "private", "chat-helper")
	l := LinkChats(t, a, b, lead, trainer, "messages")
	Inbox(t, trainer.C)
	if out := b.Hook("Stop", "chat-trainer", false); out != "" {
		t.Fatalf("nothing unhandled, stop printed %q", out)
	}

	sendChat(t, lead.C, l.ANum, "HOOK-SECRET hello")
	var out string
	Eventually(t, wait, "stop blocks", func() bool {
		out = b.Hook("Stop", "chat-trainer", false)
		return out != ""
	})
	var res struct{ Decision, Reason string }
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("stop output %q: %v", out, err)
	}
	want := fmt.Sprintf("cravv-connect: 1 new message on link %d from alice. Call check_inbox. Then start the listener again.", l.BNum)
	if res.Decision != "block" || res.Reason != want || strings.Contains(out, "HOOK-SECRET") {
		t.Fatalf("stop output %q", out)
	}
	if out := b.Hook("Stop", "chat-helper", false); out != "" {
		t.Fatalf("the other chat in the folder was blocked: %q", out)
	}
	if out := b.Hook("Stop", "chat-trainer", true); out != "" {
		t.Fatalf("stop_hook_active with the same item blocked again: %q", out)
	}
	if out := b.Hook("UserPromptSubmit", "chat-trainer", false); !strings.HasPrefix(out, fmt.Sprintf("cravv-connect: 1 new message on link %d from alice.", l.BNum)) {
		t.Fatalf("prompt notice %q", out)
	}
	Inbox(t, trainer.C)
	if out := b.Hook("Stop", "chat-trainer", false); out != "" {
		t.Fatalf("stop after check_inbox %q", out)
	}
}
