package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// daemonFake is a real ipc.Server with scripted handlers on a temp socket.
type daemonFake struct {
	t        *testing.T
	srv      *ipc.Server
	sock     string
	mu       sync.Mutex
	regs     []ipc.SessionRegisterParams
	sessions []string
	cancel   context.CancelFunc
	done     chan struct{}
}

func newDaemonFake(t *testing.T) *daemonFake {
	dir, err := os.MkdirTemp("", "mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	d := &daemonFake{t: t, srv: ipc.NewServer(ipc.Options{}), sock: filepath.Join(dir, "d.sock")}
	d.srv.Register(ipc.MethodSessionRegister, ipc.Typed(func(_ context.Context, cs *ipc.ConnState, p ipc.SessionRegisterParams) (any, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.regs = append(d.regs, p)
		name := p.Agent + "@" + filepath.Base(p.ProjectDir)
		cs.SetSession(name, p.ProjectDir)
		return ipc.SessionRegisterResult{Name: name}, nil
	}), ipc.GateNone)
	return d
}

func (d *daemonFake) handle(method string, gate ipc.Gate, fn func(cs *ipc.ConnState, raw json.RawMessage) (any, error)) {
	d.srv.Register(method, func(_ context.Context, cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		d.mu.Lock()
		d.sessions = append(d.sessions, cs.Session())
		d.mu.Unlock()
		return fn(cs, raw)
	}, gate)
}

func (d *daemonFake) start() {
	ln, err := ipc.Listen(d.sock)
	if err != nil {
		d.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel, d.done = cancel, make(chan struct{})
	go func() { d.srv.Serve(ctx, ln); close(d.done) }()
	d.t.Cleanup(d.stop)
}

func (d *daemonFake) stop() {
	if d.cancel != nil {
		d.cancel()
		<-d.done
		d.cancel = nil
	}
}

func (d *daemonFake) registrations() []ipc.SessionRegisterParams {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.regs)
}

// connect runs the MCP server against d over in-memory transports and returns
// a client session named clientName.
func connect(t *testing.T, d *daemonFake, clientName string) (*mcp.ClientSession, *Session) {
	t.Helper()
	ctx := context.Background()
	srv, sess := New(Options{
		Dial:       func(ctx context.Context) (Conn, error) { return ipc.DialContext(ctx, d.sock) },
		ProjectDir: "/work/glow-v2",
		Version:    "test",
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Wait(); sess.Close() })
	return cs, sess
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", name, err)
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n"), res.IsError
}

func TestRegistersLazilyWithClientName(t *testing.T) {
	d := newDaemonFake(t)
	d.handle(ipc.MethodKill, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) { return nil, nil })
	d.start()
	cs, sess := connect(t, d, "claude-code")
	if _, isErr := callTool(t, cs, "kill_switch", nil); isErr {
		t.Fatal("kill_switch failed")
	}
	regs := d.registrations()
	if len(regs) != 1 {
		t.Fatalf("registrations %d", len(regs))
	}
	if r := regs[0]; r.Agent != "claude" || r.ProjectDir != "/work/glow-v2" || r.PID != os.Getpid() {
		t.Fatalf("registration %+v", r)
	}
	if sess.Name() != "claude@glow-v2" {
		t.Fatalf("name %q", sess.Name())
	}
}

func TestOnInitializedRegistersEagerly(t *testing.T) {
	d := newDaemonFake(t)
	d.start()
	sess := NewSession(func(ctx context.Context) (Conn, error) { return ipc.DialContext(ctx, d.sock) }, "/work/glow-v2")
	defer sess.Close()
	onInitialized(context.Background(), sess, &mcp.InitializeParams{ClientInfo: &mcp.Implementation{Name: "codex-mcp-client"}}, slog.New(slog.DiscardHandler))
	if regs := d.registrations(); len(regs) != 1 || regs[0].Agent != "codex" || sess.Name() != "codex@glow-v2" {
		t.Fatalf("regs %+v name %q", regs, sess.Name())
	}
}

func TestToolListAndDescriptions(t *testing.T) {
	d := newDaemonFake(t)
	d.start()
	cs, _ := connect(t, d, "codex-mcp-client")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || strings.ContainsRune(tool.Description, '\u2014') {
			t.Errorf("%s: bad description %q", tool.Name, tool.Description)
		}
	}
	want := []string{"cancel_task", "check_inbox", "claim_task", "complete_task", "connect", "create_task", "disconnect",
		"fail_task", "get_task", "kill_switch", "links", "pause_peer", "restrict", "send_file", "send_message",
		"session_close", "session_share", "sessions", "status", "unpair_peer", "update_task", "wait_for_message"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatalf("tools %v", names)
	}
	if got := cs.InitializeResult().Instructions; got == "" {
		t.Fatal("no instructions sent")
	}
}

func TestInboxToolsReturnWrappedText(t *testing.T) {
	d := newDaemonFake(t)
	var limits []int
	d.handle(ipc.MethodInboxCheck, ipc.GateSession, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.InboxCheckParams
		json.Unmarshal(raw, &p)
		limits = append(limits, p.Limit)
		if p.Limit == 1 {
			return ipc.InboxResult{Items: []ipc.InboxView{}}, nil
		}
		return ipc.InboxResult{Items: []ipc.InboxView{
			{Wrapped: `<remote_message from="gpu-box" kind="chat">hi</remote_message>`},
			{Wrapped: `<remote_message from="gpu-box" kind="task">do</remote_message>`},
		}}, nil
	})
	d.handle(ipc.MethodInboxWait, ipc.GateSession, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		return ipc.InboxResult{}, nil
	})
	d.start()
	cs, _ := connect(t, d, "claude-code")
	text, isErr := callTool(t, cs, "check_inbox", nil)
	want := "<remote_message from=\"gpu-box\" kind=\"chat\">hi</remote_message>\n\n<remote_message from=\"gpu-box\" kind=\"task\">do</remote_message>"
	if isErr || text != want {
		t.Fatalf("%v %q", isErr, text)
	}
	if text, _ := callTool(t, cs, "check_inbox", map[string]any{"limit": 1}); text != NoMessagesText {
		t.Fatalf("empty: %q", text)
	}
	if text, _ := callTool(t, cs, "wait_for_message", map[string]any{"timeout_s": 1}); text != NothingYetText {
		t.Fatalf("wait: %q", text)
	}
	if !slices.Equal(limits, []int{0, 1}) {
		t.Fatalf("limits %v", limits)
	}
}

func TestStructuredToolsReturnJSON(t *testing.T) {
	d := newDaemonFake(t)
	var created ipc.TaskCreateParams
	d.handle(ipc.MethodTaskCreate, ipc.GateSession, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		json.Unmarshal(raw, &created)
		return ipc.TaskCreateResult{TaskID: "T1"}, nil
	})
	d.handle(ipc.MethodTaskClaim, ipc.GateSession, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return nil, core.ErrAlreadyClaimed
	})
	d.handle(ipc.MethodStatus, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.StatusResult{MachineID: "m1", RelayConnected: true}, nil
	})
	d.start()
	cs, _ := connect(t, d, "claude-code")
	text, isErr := callTool(t, cs, "create_task", map[string]any{"link": 3, "instructions": "train", "file_paths": []string{"a.py"}})
	if isErr || text != "{\n  \"task_id\": \"T1\"\n}" {
		t.Fatalf("%v %q", isErr, text)
	}
	if created.Link != 3 || created.Instructions != "train" || !slices.Equal(created.FilePaths, []string{"a.py"}) {
		t.Fatalf("params %+v", created)
	}
	text, isErr = callTool(t, cs, "claim_task", map[string]any{"task_id": "T1"})
	if !isErr || text != "task already claimed" {
		t.Fatalf("claim: %v %q", isErr, text)
	}
	text, _ = callTool(t, cs, "status", nil)
	var st statusOut
	if err := json.Unmarshal([]byte(text), &st); err != nil || st.Session != "claude@glow-v2" || st.MachineID != "m1" {
		t.Fatalf("status %v %q", err, text)
	}
	if _, isErr := callTool(t, cs, "create_task", map[string]any{"link": 3}); !isErr {
		t.Fatal("missing required instructions accepted")
	}
}

func TestRestrictExplainsRaise(t *testing.T) {
	d := newDaemonFake(t)
	d.handle(ipc.MethodLinkRestrict, ipc.GateNone, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.LinkPermissionParams
		json.Unmarshal(raw, &p)
		if p.Permission == "tasks-auto" {
			return nil, core.ErrAuthRequired
		}
		return ipc.LinkView{Link: p.Link, PermissionIn: p.Permission}, nil
	})
	d.start()
	cs, _ := connect(t, d, "claude-code")
	if text, isErr := callTool(t, cs, "restrict", map[string]any{"link": 3, "permission": "messages"}); isErr || text != "Link 3 now allows messages." {
		t.Fatalf("%v %q", isErr, text)
	}
	text, isErr := callTool(t, cs, "restrict", map[string]any{"link": 3, "permission": "tasks-auto"})
	if !isErr || !strings.Contains(text, "human's password") {
		t.Fatalf("%v %q", isErr, text)
	}
}

// session_share hands the model the wake token but never the reattach
// token; the MCP server keeps that and takes the session back after the
// daemon restarts.
func TestShareKeepsReattachTokenForReconnects(t *testing.T) {
	d := newDaemonFake(t)
	var mu sync.Mutex
	var reattached []string
	d.handle(ipc.MethodSessionShare, ipc.GateSession, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		return ipc.ShareResult{Session: ipc.SharedSessionView{Name: "lead", State: "open"}, WakeToken: "WAKE", ReattachToken: "SECRET-REATTACH"}, nil
	})
	d.handle(ipc.MethodSessionReattach, ipc.GateSession, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.SessionReattachParams
		json.Unmarshal(raw, &p)
		mu.Lock()
		reattached = append(reattached, p.ReattachToken)
		mu.Unlock()
		return ipc.SharedSessionView{Name: "lead", State: "open"}, nil
	})
	d.handle(ipc.MethodLinks, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.LinksResult{Links: []ipc.LinkView{}}, nil
	})
	d.start()
	cs, _ := connect(t, d, "claude-code")
	text, isErr := callTool(t, cs, "session_share", map[string]any{"name": "lead"})
	if isErr || !strings.Contains(text, `"wake_token": "WAKE"`) || strings.Contains(text, "SECRET-REATTACH") {
		t.Fatalf("share output %v %q", isErr, text)
	}
	d.stop()
	d.start()
	if _, isErr := callTool(t, cs, "links", nil); isErr {
		t.Fatal("links after the daemon restarted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reattached) != 1 || reattached[0] != "SECRET-REATTACH" {
		t.Fatalf("reattached with %v", reattached)
	}
}

func TestDaemonDownThenUp(t *testing.T) {
	d := newDaemonFake(t)
	d.handle(ipc.MethodChatSend, ipc.GateSession, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.IDResult{ID: "M1"}, nil
	})
	cs, _ := connect(t, d, "claude-code") // daemon not started yet
	text, isErr := callTool(t, cs, "send_message", map[string]any{"link": 3, "text": "hi"})
	if !isErr || text != "daemon not running: run `cravv-connect daemon start`" {
		t.Fatalf("down: %v %q", isErr, text)
	}
	d.start()
	text, isErr = callTool(t, cs, "send_message", map[string]any{"link": 3, "text": "hi"})
	if isErr || !strings.Contains(text, `"id": "M1"`) {
		t.Fatalf("up: %v %q", isErr, text)
	}
}

func TestReconnectsAfterDaemonRestart(t *testing.T) {
	d := newDaemonFake(t)
	d.handle(ipc.MethodKill, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) { return nil, nil })
	d.start()
	cs, sess := connect(t, d, "claude-code")
	if _, isErr := callTool(t, cs, "kill_switch", nil); isErr {
		t.Fatal("first call failed")
	}
	d.stop()
	// kill_switch is not retried on a dropped connection, so wait until the
	// client has seen the old one close (under load that can lag the stop).
	sess.mu.Lock()
	old := sess.conn
	sess.mu.Unlock()
	select {
	case <-old.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the old connection never closed")
	}
	d.start()
	text, isErr := callTool(t, cs, "kill_switch", nil)
	if isErr || !strings.HasPrefix(text, "Kill switch is on.") {
		t.Fatalf("%v %q", isErr, text)
	}
	if n := len(d.registrations()); n != 2 {
		t.Fatalf("registrations %d, want 2 (re-registered after restart)", n)
	}
}

func TestNormalizeAgent(t *testing.T) {
	for in, want := range map[string]string{
		"claude-code": "claude", "Claude Code": "claude", "codex-mcp-client": "codex", "cursor-vscode": "cursor",
		"Visual Studio Code": "vscode", "gemini-cli-mcp-client": "gemini", "": "agent", "!!!": "agent",
		"My Very Long Custom Agent Name": "my-very-long-cus", "</x>": "x",
	} {
		if got := normalizeAgent(in); got != want {
			t.Errorf("normalizeAgent(%q) = %q, want %q", in, got, want)
		}
	}
}
