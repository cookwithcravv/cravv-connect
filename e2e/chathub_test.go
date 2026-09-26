package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/mcpserver"
)

// Human answers elicitation forms in the test's place. Each form is
// answered by the next function in its script.
type Human struct {
	mu     sync.Mutex
	script []func(*mcp.ElicitParams) *mcp.ElicitResult
	forms  []*mcp.ElicitParams
}

// Answer queues answers.
func (h *Human) Answer(fns ...func(*mcp.ElicitParams) *mcp.ElicitResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.script = append(h.script, fns...)
}

// Forms returns the forms shown so far.
func (h *Human) Forms() []*mcp.ElicitParams {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*mcp.ElicitParams(nil), h.forms...)
}

func (h *Human) handle(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.forms = append(h.forms, req.Params)
	if len(h.script) == 0 {
		return &mcp.ElicitResult{Action: "cancel"}, nil
	}
	next := h.script[0]
	h.script = h.script[1:]
	return next(req.Params), nil
}

// Choose answers a form with one of its choices; Dismiss with a bare action.
func Choose(choice string) func(*mcp.ElicitParams) *mcp.ElicitResult {
	return func(*mcp.ElicitParams) *mcp.ElicitResult {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": choice}}
	}
}

func Dismiss(action string) func(*mcp.ElicitParams) *mcp.ElicitResult {
	return func(*mcp.ElicitParams) *mcp.ElicitResult { return &mcp.ElicitResult{Action: action} }
}

// formChoices returns the choices a form offered.
func formChoices(t *testing.T, p *mcp.ElicitParams) []string {
	t.Helper()
	b, _ := json.Marshal(p.RequestedSchema)
	var s struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s.Properties["decision"].Enum
}

// newClaudeAgent is an MCP client like Claude Code in a terminal: it can
// show forms (answered by h; nil for none), its MCP server knows the chat
// ID, and session_share writes a wake file in the node's state folder.
func newClaudeAgent(t *testing.T, n *Node, chatID string, h *Human) (*mcpAgent, *[]string) {
	t.Helper()
	ctx := context.Background()
	srv, sess := mcpserver.New(mcpserver.Options{
		Dial:         func(ctx context.Context) (mcpserver.Conn, error) { return ipc.DialContext(ctx, n.Paths.Socket) },
		ProjectDir:   n.Proj,
		Version:      "e2e",
		AgentSession: chatID,
		WakeDir:      filepath.Join(n.Paths.Home, "wake"),
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	var opts *mcp.ClientOptions
	if h != nil {
		opts = &mcp.ClientOptions{ElicitationHandler: h.handle}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "1"}, opts)
	cs, err := client.Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Wait(); sess.Close() })
	seen := &[]string{}
	return &mcpAgent{t: t, cs: cs, seen: seen}, seen
}

// shareAndListen shares the chat and starts the listener command it
// returns, as the agent does, and returns the command's wake file.
func shareAndListen(t *testing.T, n *Node, m *mcpAgent, args map[string]any) (string, <-chan CLIRun) {
	t.Helper()
	var out struct {
		Listener  string `json:"listener"`
		WakeToken string `json:"wake_token"`
	}
	m.decode("session_share", args, &out)
	file, ok := strings.CutPrefix(out.Listener, "cravv-connect listen --wake-file ")
	if !ok || out.WakeToken != "" {
		t.Fatalf("share returned %+v", out)
	}
	return file, n.listenFile(file)
}

// listenFile runs `cravv-connect listen --wake-file file` in the background.
func (n *Node) listenFile(file string) <-chan CLIRun {
	done := make(chan CLIRun, 1)
	go func() { done <- n.RunCLI("", "listen", "--wake-file", file) }()
	return done
}

// v2 Phase 2 end to end through the real MCP server and daemons: share,
// the listener wakes the chat, review_pending asks the human in a form
// (never offering tasks-auto), a dismissed form falls back to a desktop
// code the model never sees, an approved task reaches the chat once, the
// sender's listener wakes for updates, and the Stop hook keeps a chat with
// unhandled items going.
func TestChatHubEndToEnd(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	human := &Human{}
	mb, bSeen := newClaudeAgent(t, b, "chat-bob", human)
	ma, _ := newClaudeAgent(t, a, "chat-alice", nil)

	wakeB, bl := shareAndListen(t, b, mb, map[string]any{"name": "trainer", "purpose": "trains", "visibility": "all-peers"})
	wakeA, al := shareAndListen(t, a, ma, map[string]any{"name": "lead"})
	var out ipc.LinkView
	ma.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-auto", "note": "HUB-NOTE"}, &out)

	// The request wakes bob's chat; the form never offers tasks-auto.
	r := Waited(t, bl, wait, "link request")
	in := b.WaitLink(wait, "request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	if want := fmt.Sprintf("cravv-connect: 1 link request on link %d from alice. Call check_inbox, then review_pending.\n", in.Link); r.Stdout != want {
		t.Fatalf("listener %q, want %q", r.Stdout, want)
	}
	mb.call("check_inbox", nil)
	human.Answer(Choose("accept as tasks-ask"))
	if text := mb.call("review_pending", nil); !strings.Contains(text, "accepted") || !strings.Contains(text, "tasks-ask") {
		t.Fatalf("review_pending %q", text)
	}
	forms := human.Forms()
	if len(forms) != 1 || !strings.Contains(forms[0].Message, "HUB-NOTE") {
		t.Fatalf("forms %+v", forms)
	}
	for _, c := range formChoices(t, forms[0]) {
		if strings.Contains(c, "tasks-auto") {
			t.Fatalf("the form offers %q", c)
		}
	}
	a.WaitLink(wait, "accepted at tasks-ask", func(l ipc.LinkView) bool {
		return l.Link == out.Link && l.State == "active" && l.PermissionOut == "tasks-ask"
	})
	Waited(t, al, wait, "accepted notice at alice")
	ma.call("check_inbox", nil)

	// A task on the tasks-ask link: bob's human dismisses the form, so a
	// code appears on bob's desktop; the human types it in the chat.
	bl = b.listenFile(wakeB)
	var created ipc.TaskCreateResult
	ma.decode("create_task", map[string]any{"link": out.Link, "instructions": "HUB-SECRET-TASK sort the data"}, &created)
	r = Waited(t, bl, wait, "task awaiting approval")
	if !strings.Contains(r.Stdout, "1 task awaiting approval on link") {
		t.Fatalf("listener %q", r.Stdout)
	}
	if text := mb.call("check_inbox", nil); strings.Contains(text, "HUB-SECRET-TASK") {
		t.Fatalf("an unapproved task reached the model: %q", text)
	}
	human.Answer(Dismiss("decline"))
	text := mb.call("review_pending", nil)
	item := "task-" + created.TaskID
	code := b.Desktop.Code()
	if !strings.Contains(text, "4-digit code") || strings.Contains(text, "HUB-SECRET-TASK") || len(code) != 4 {
		t.Fatalf("review_pending %q code %q", text, code)
	}
	if forms := human.Forms(); len(forms) != 2 || !strings.Contains(forms[1].Message, "HUB-SECRET-TASK sort the data") {
		t.Fatalf("the human's form lacks the task: %+v", forms)
	}
	bl = b.listenFile(wakeB)
	if text := mb.call("review_pending", map[string]any{"item": item, "decision": "accept", "code": code}); !strings.Contains(text, "approved") {
		t.Fatalf("typed code %q", text)
	}
	r = Waited(t, bl, wait, "approved task")
	if !strings.Contains(r.Stdout, "1 new task on link") {
		t.Fatalf("listener %q", r.Stdout)
	}
	if text := mb.call("check_inbox", nil); !strings.Contains(text, "HUB-SECRET-TASK") {
		t.Fatalf("approved task not delivered: %q", text)
	}

	// The worker finishes; the sender's listener wakes for the updates.
	al = a.listenFile(wakeA)
	mb.call("claim_task", map[string]any{"task_id": created.TaskID})
	r = Waited(t, al, wait, "task update at alice")
	if !strings.Contains(r.Stdout, "new task update") {
		t.Fatalf("sender listener %q", r.Stdout)
	}
	mb.call("complete_task", map[string]any{"task_id": created.TaskID, "result": "done"})

	// A chat message bob's chat has not read keeps it from stopping, once.
	ma.call("send_message", map[string]any{"link": out.Link, "text": "HUB-CHAT"})
	var stop string
	Eventually(t, wait, "stop blocks", func() bool { stop = b.Hook("Stop", "chat-bob", false); return stop != "" })
	if !strings.Contains(stop, `"decision":"block"`) || strings.Contains(stop, "HUB-CHAT") {
		t.Fatalf("stop %q", stop)
	}
	if again := b.Hook("Stop", "chat-bob", true); again != "" {
		t.Fatalf("blocked again for the same item: %q", again)
	}

	// The model never saw the confirmation code.
	shown := regexp.MustCompile(`(^|[^0-9A-Za-z])` + code + `([^0-9A-Za-z]|$)`)
	for _, s := range *bSeen {
		if shown.MatchString(s) {
			t.Fatalf("a tool result showed the code %s: %q", code, s)
		}
	}
}
