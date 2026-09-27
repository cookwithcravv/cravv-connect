package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/mcpserver"
)

// mcpAgent is an MCP client (clientInfo "claude-code") talking to the real
// MCP server, which talks to node's daemon over its IPC socket.
type mcpAgent struct {
	t    *testing.T
	cs   *mcp.ClientSession
	seen *[]string // every tool result the model saw (nil: not kept)
}

func newMCPAgent(t *testing.T, n *Node) *mcpAgent {
	t.Helper()
	m, _ := newMCPAgentStoppable(t, n)
	return m
}

// newMCPAgentStoppable is newMCPAgent plus a function that ends the MCP
// server and its daemon connection, as when Claude Code exits.
func newMCPAgentStoppable(t *testing.T, n *Node) (*mcpAgent, func()) {
	t.Helper()
	ctx := context.Background()
	srv, sess := mcpserver.New(mcpserver.Options{
		Dial:       func(ctx context.Context) (mcpserver.Conn, error) { return ipc.DialContext(ctx, n.Paths.Socket) },
		ProjectDir: n.Proj,
		Version:    "e2e",
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { cs.Close(); ss.Wait(); sess.Close() }) }
	t.Cleanup(stop)
	return &mcpAgent{t: t, cs: cs}, stop
}

// try calls a tool and returns its text and whether it reported an error.
func (m *mcpAgent) try(name string, args map[string]any) (string, bool) {
	m.t.Helper()
	res, err := m.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		m.t.Fatalf("%s: %v", name, err)
	}
	var buf bytes.Buffer
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			buf.WriteString(tc.Text)
		}
	}
	if m.seen != nil {
		*m.seen = append(*m.seen, buf.String())
	}
	return buf.String(), res.IsError
}

func (m *mcpAgent) call(name string, args map[string]any) string {
	m.t.Helper()
	out, isErr := m.try(name, args)
	if isErr {
		m.t.Fatalf("%s failed: %s", name, out)
	}
	return out
}

// decode calls a tool and decodes its JSON result into v.
func (m *mcpAgent) decode(name string, args map[string]any, v any) {
	m.t.Helper()
	out := m.call(name, args)
	if err := json.Unmarshal([]byte(out), v); err != nil {
		m.t.Fatalf("%s output %q: %v", name, out, err)
	}
}

// onlyWrapped fails unless peer-authored text appears in out only inside the
// <remote_message> wrapper, never in a plain field.
func onlyWrapped(t *testing.T, what, out, text string) {
	t.Helper()
	var tv ipc.TaskView
	if err := json.Unmarshal([]byte(out), &tv); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !strings.Contains(tv.Wrapped, "<remote_message") || !strings.Contains(tv.Wrapped, text) {
		t.Fatalf("%s: %q not inside the wrapper: %s", what, text, out)
	}
	tv.Wrapped = ""
	plain, _ := json.Marshal(tv)
	if strings.Contains(string(plain), text) {
		t.Fatalf("%s: peer text %q outside the wrapper: %s", what, text, plain)
	}
}

// Every agent-facing MCP tool through the real MCP server and real daemons:
// share, discover, connect (the human accepts), then chat, tasks and files
// over the link, restrict and disconnect.
func TestMCPToolsEndToEnd(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	ma, mb := newMCPAgent(t, a), newMCPAgent(t, b)

	if msg, isErr := ma.try("send_message", map[string]any{"link": 1, "text": "x"}); !isErr || !strings.Contains(msg, "session_share") {
		t.Fatalf("send before sharing: %v %q", isErr, msg)
	}
	var share struct {
		Session   ipc.SharedSessionView `json:"session"`
		WakeToken string                `json:"wake_token"`
	}
	ma.decode("session_share", map[string]any{"name": "lead", "purpose": "coordinates"}, &share)
	if share.Session.Name != "lead" || share.Session.Visibility != "private" || share.WakeToken == "" {
		t.Fatalf("share %+v", share)
	}
	mb.call("session_share", map[string]any{"name": "trainer", "purpose": "MCP-PURPOSE trains", "visibility": "all-peers"})

	var listed ipc.SessionsListResult
	ma.decode("sessions", map[string]any{"machine": "bob"}, &listed)
	if len(listed.Sessions) != 1 || listed.Sessions[0].Name != "trainer" || !strings.Contains(listed.Sessions[0].Wrapped, "MCP-PURPOSE trains") {
		t.Fatalf("sessions %+v", listed)
	}
	var out ipc.LinkView
	ma.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-auto", "note": "MCP-NOTE please"}, &out)
	in := b.WaitLink(wait, "request at bob", func(v ipc.LinkView) bool { return v.State == "pending" && v.Direction == "in" })
	b.Decide(in.Link, true, "")
	var links ipc.LinksResult
	Eventually(t, wait, "alice's link active", func() bool {
		ma.decode("links", nil, &links)
		return len(links.Links) == 1 && links.Links[0].State == "active" && links.Links[0].PermissionOut == "tasks-auto"
	})

	// Chat both ways.
	ma.call("send_message", map[string]any{"link": out.Link, "text": "MCP-CHAT hello"})
	var inbox string
	Eventually(t, wait, "chat in bob's MCP inbox", func() bool {
		inbox += mb.call("check_inbox", map[string]any{})
		return strings.Contains(inbox, "MCP-CHAT hello")
	})
	if !strings.Contains(inbox, `<remote_message from="alice" session="lead"`) {
		t.Fatalf("check_inbox output %q", inbox)
	}

	// create_task, then the worker finds it, claims, updates and completes it.
	const instr = "MCP-INSTR sort the dataset"
	var created ipc.TaskCreateResult
	ma.decode("create_task", map[string]any{"link": out.Link, "instructions": instr}, &created)
	Eventually(t, wait, "task in bob's MCP inbox", func() bool {
		inbox += mb.call("check_inbox", map[string]any{})
		return strings.Contains(inbox, created.TaskID)
	})
	onlyWrapped(t, "worker get_task", mb.call("get_task", map[string]any{"task_id": created.TaskID}), instr)
	var tv ipc.TaskView
	mb.decode("claim_task", map[string]any{"task_id": created.TaskID}, &tv)
	if tv.State != "claimed" {
		t.Fatalf("claim_task: %+v", tv)
	}
	mb.decode("update_task", map[string]any{"task_id": created.TaskID, "note": "MCP-NOTE halfway"}, &tv)
	mb.decode("complete_task", map[string]any{"task_id": created.TaskID, "result": "MCP-RESULT sorted"}, &tv)
	if tv.State != "done" {
		t.Fatalf("complete_task: %+v", tv)
	}
	Eventually(t, wait, "sender sees the result", func() bool {
		ma.decode("get_task", map[string]any{"task_id": created.TaskID}, &tv)
		return tv.State == "done" && strings.Contains(tv.Wrapped, "MCP-RESULT sorted")
	})
	senderView := ma.call("get_task", map[string]any{"task_id": created.TaskID})
	onlyWrapped(t, "sender get_task (result)", senderView, "MCP-RESULT sorted")
	onlyWrapped(t, "sender get_task (note)", senderView, "MCP-NOTE halfway")

	// send_file.
	data := []byte("MCP-FILE contents\n")
	if err := os.WriteFile(filepath.Join(a.Proj, "mcp.txt"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var sent ipc.FileSendResult
	ma.decode("send_file", map[string]any{"link": out.Link, "path": "mcp.txt"}, &sent)
	Eventually(t, wait, "file notice in bob's MCP inbox", func() bool {
		inbox += mb.call("check_inbox", map[string]any{})
		return strings.Contains(inbox, "mcp.txt") && strings.Contains(inbox, "saved to")
	})
	if msg, isErr := ma.try("send_file", map[string]any{"link": out.Link, "path": ".env"}); !isErr {
		t.Fatalf("send_file of a secret succeeded: %s", msg)
	}

	// restrict lowers; raising is refused with the password hint.
	var bl ipc.LinksResult
	mb.decode("links", nil, &bl)
	if text := mb.call("restrict", map[string]any{"link": bl.Links[0].Link, "permission": "messages"}); !strings.Contains(text, "now allows messages") {
		t.Fatalf("restrict: %q", text)
	}
	if text, isErr := mb.try("restrict", map[string]any{"link": bl.Links[0].Link, "permission": "tasks-auto"}); !isErr || !strings.Contains(text, "password") {
		t.Fatalf("raise: %v %q", isErr, text)
	}

	// disconnect closes both sides.
	ma.call("disconnect", map[string]any{"link": out.Link})
	b.WaitLink(wait, "bob's side closed", func(v ipc.LinkView) bool { return v.Link == in.Link && v.State == "closed" })
	if msg, isErr := ma.try("send_message", map[string]any{"link": out.Link, "text": "x"}); !isErr {
		t.Fatalf("send on a closed link: %s", msg)
	}
}

// After the daemon restarts, the MCP server reconnects and takes its session
// back with the reattach token it kept: the link survives and the chat still
// receives.
func TestMCPReattachesAfterDaemonRestart(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	ma := newMCPAgent(t, a)
	ma.call("session_share", map[string]any{"name": "lead"})
	sb := b.Share("claude", "trainer", "all-peers")
	var out ipc.LinkView
	ma.decode("connect", map[string]any{"target": "bob/trainer", "permission": "messages"}, &out)
	in := b.WaitLink(wait, "request at bob", func(v ipc.LinkView) bool { return v.State == "pending" && v.Direction == "in" })
	b.Decide(in.Link, true, "")
	a.WaitLink(wait, "link active", func(v ipc.LinkView) bool { return v.Link == out.Link && v.State == "active" })

	a.Restart()
	a.WaitOnline()
	var links ipc.LinksResult
	ma.decode("links", nil, &links) // reconnects, re-registers and reattaches
	if len(links.Links) != 1 || links.Links[0].State != "active" {
		t.Fatalf("links after restart %+v", links.Links)
	}
	sendChat(t, sb.C, in.Link, "after the restart")
	var inbox string
	Eventually(t, wait, "chat after the restart", func() bool {
		inbox += ma.call("check_inbox", map[string]any{})
		return strings.Contains(inbox, "after the restart")
	})
}

// The MCP server does not wait for the chat's next tool call to take its
// session back: when the daemon restarts while the chat is idle, the
// session is open again within seconds (long before the away grace), and
// its link is intact on both sides.
func TestMCPReattachesInTheBackground(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	ma := newMCPAgent(t, a)
	ma.call("session_share", map[string]any{"name": "lead"})
	sb := b.Share("claude", "trainer", "all-peers")
	var out ipc.LinkView
	ma.decode("connect", map[string]any{"target": "bob/trainer", "permission": "messages"}, &out)
	in := b.WaitLink(wait, "request at bob", func(v ipc.LinkView) bool { return v.State == "pending" && v.Direction == "in" })
	b.Decide(in.Link, true, "")
	a.WaitLink(wait, "link active", func(v ipc.LinkView) bool { return v.Link == out.Link && v.State == "active" })

	a.Restart()
	start := time.Now()
	Eventually(t, 15*time.Second, "lead open again without a tool call", func() bool {
		return slices.Contains(a.Status().Sessions, "lead (open)")
	})
	t.Logf("open again after %s", time.Since(start).Round(time.Millisecond))
	a.WaitLink(wait, "alice's link active", func(v ipc.LinkView) bool { return v.Link == out.Link && v.State == "active" })
	b.WaitLink(wait, "bob sees lead back", func(v ipc.LinkView) bool { return v.Link == in.Link && v.State == "active" && !v.RemoteAway })
	sendChat(t, sb.C, in.Link, "back in the background")
	var inbox string
	Eventually(t, wait, "chat after the restart", func() bool {
		inbox += ma.call("check_inbox", map[string]any{})
		return strings.Contains(inbox, "back in the background")
	})
}

// Claude Code restarts: the old MCP server is gone (its session is away)
// and the new chat in the same folder shares the same name again. It takes
// the away session over: same link, what queued arrives, and the peer sees
// it back. A second chat cannot take the name while it is open.
func TestShareAgainAfterClaudeCodeRestart(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	first, stop := newMCPAgentStoppable(t, a)
	first.call("session_share", map[string]any{"name": "lead", "purpose": "leads", "visibility": "all-peers"})
	sb := b.Share("claude", "trainer", "all-peers")
	var out ipc.LinkView
	first.decode("connect", map[string]any{"target": "bob/trainer", "permission": "messages"}, &out)
	in := b.WaitLink(wait, "request at bob", func(v ipc.LinkView) bool { return v.State == "pending" && v.Direction == "in" })
	b.Decide(in.Link, true, "")
	a.WaitLink(wait, "link active", func(v ipc.LinkView) bool { return v.Link == out.Link && v.State == "active" })

	stop() // Claude Code exits
	b.WaitLink(wait, "bob sees lead away", func(v ipc.LinkView) bool { return v.Link == in.Link && v.RemoteAway })
	sendChat(t, sb.C, in.Link, "while you restarted")

	second := newMCPAgent(t, a)
	text := second.call("session_share", map[string]any{"name": "lead"})
	if !strings.Contains(text, `"resumed": true`) || !strings.Contains(text, `"visibility": "all-peers"`) {
		t.Fatalf("share again: %s", text)
	}
	var links ipc.LinksResult
	second.decode("links", nil, &links)
	if len(links.Links) != 1 || links.Links[0].Link != out.Link || links.Links[0].State != "active" {
		t.Fatalf("links after the takeover %+v", links.Links)
	}
	b.WaitLink(wait, "bob sees lead back", func(v ipc.LinkView) bool { return v.Link == in.Link && !v.RemoteAway })
	var inbox string
	Eventually(t, wait, "what queued", func() bool {
		inbox += second.call("check_inbox", map[string]any{})
		return strings.Contains(inbox, "while you restarted")
	})

	third := newMCPAgent(t, a)
	if text, isErr := third.try("session_share", map[string]any{"name": "lead"}); !isErr ||
		!strings.Contains(text, "a session named lead is open in another chat; close it there or pick another name") {
		t.Fatalf("a second chat took an open name: %v %s", isErr, text)
	}
}
