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
	return connectWith(t, d, clientName, Options{}, nil, "")
}

// connectWith is connect with extra server options, client options and a
// protocol version ("" for the SDK's latest).
func connectWith(t *testing.T, d *daemonFake, clientName string, opts Options, copts *mcp.ClientOptions, proto string) (*mcp.ClientSession, *Session) {
	t.Helper()
	ctx := context.Background()
	opts.Dial = func(ctx context.Context) (Conn, error) { return ipc.DialContext(ctx, d.sock) }
	opts.ProjectDir, opts.Version, opts.AgentSession = "/work/glow-v2", "test", "chat-1"
	srv, sess := New(opts)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "1"}, copts)
	cs, err := client.Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: proto})
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
		"fail_task", "get_task", "kill_switch", "links", "machines", "restrict", "review_pending", "send_file", "send_message",
		"session_close", "session_set", "session_share", "sessions", "update_task", "wait_for_message"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatalf("tools %v", names)
	}
	hints := map[string]*mcp.ToolAnnotations{}
	for _, tool := range res.Tools {
		hints[tool.Name] = tool.Annotations
	}
	for _, n := range []string{"machines", "sessions", "links", "check_inbox", "wait_for_message", "get_task"} {
		if a := hints[n]; a == nil || !a.ReadOnlyHint {
			t.Errorf("%s should be read-only: %+v", n, a)
		}
	}
	// review_pending sends the human's decision to the peer (link accepted,
	// task approved or denied).
	for _, n := range []string{"connect", "send_message", "create_task", "send_file", "update_task", "complete_task", "fail_task", "review_pending"} {
		if a := hints[n]; a == nil || a.ReadOnlyHint || a.OpenWorldHint == nil || !*a.OpenWorldHint {
			t.Errorf("%s sends to another machine: %+v", n, a)
		}
	}
	for _, n := range []string{"session_share", "session_set", "links", "check_inbox"} {
		if a := hints[n]; a == nil || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s stays on this machine: %+v", n, a)
		}
	}
	for _, n := range []string{"disconnect", "session_close", "kill_switch"} {
		if a := hints[n]; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Errorf("%s is a cut-off: %+v", n, a)
		}
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
	var waits []int
	d.handle(ipc.MethodInboxWait, ipc.GateSession, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.InboxWaitParams
		json.Unmarshal(raw, &p)
		waits = append(waits, p.TimeoutS)
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
	callTool(t, cs, "wait_for_message", map[string]any{"timeout_s": 600})
	if !slices.Equal(waits, []int{1, 600}) {
		t.Fatalf("waits %v: the tool passes the timeout on, the daemon caps it", waits)
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
	d.handle(ipc.MethodMachines, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.PeerListResult{Peers: []ipc.PeerView{{Alias: "gpu-box", Online: true}}}, nil
	})
	var set ipc.SessionSetParams
	d.handle(ipc.MethodSessionSet, ipc.GateSession, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		json.Unmarshal(raw, &set)
		return ipc.SharedSessionView{Name: "lead", Purpose: *set.Purpose, Visibility: "private"}, nil
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
	text, _ = callTool(t, cs, "machines", nil)
	var pl ipc.PeerListResult
	if err := json.Unmarshal([]byte(text), &pl); err != nil || len(pl.Peers) != 1 || pl.Peers[0].Alias != "gpu-box" {
		t.Fatalf("machines %v %q", err, text)
	}
	text, isErr = callTool(t, cs, "session_set", map[string]any{"purpose": "trains models"})
	if isErr || set.Purpose == nil || *set.Purpose != "trains models" || set.Visibility != nil || !strings.Contains(text, `"purpose": "trains models"`) {
		t.Fatalf("session_set %v %q %+v", isErr, text, set)
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
	var reattached, chats []string
	d.handle(ipc.MethodSessionShare, ipc.GateSession, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.SessionShareParams
		json.Unmarshal(raw, &p)
		mu.Lock()
		chats = append(chats, p.AgentSession)
		mu.Unlock()
		return ipc.ShareResult{Session: ipc.SharedSessionView{Name: "lead", State: "open"}, WakeToken: "WAKE", ReattachToken: "SECRET-REATTACH"}, nil
	})
	d.handle(ipc.MethodSessionReattach, ipc.GateSession, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.SessionReattachParams
		json.Unmarshal(raw, &p)
		mu.Lock()
		reattached = append(reattached, p.ReattachToken)
		chats = append(chats, p.AgentSession)
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
	if len(chats) != 2 || chats[0] != "chat-1" || chats[1] != "chat-1" {
		t.Fatalf("the agent's chat ID must reach share and reattach: %v", chats)
	}
}

// A reattach that fails for a passing reason (here the kill switch) keeps
// the token and is tried again on the next call; only not_found (the session
// closed meanwhile) forgets it.
func TestReattachForgetsTokenOnlyWhenNotFound(t *testing.T) {
	d := newDaemonFake(t)
	var mu sync.Mutex
	var reattached []string
	var fail error
	d.handle(ipc.MethodSessionShare, ipc.GateSession, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		return ipc.ShareResult{Session: ipc.SharedSessionView{Name: "lead", State: "open"}, WakeToken: "WAKE", ReattachToken: "R"}, nil
	})
	d.handle(ipc.MethodSessionReattach, ipc.GateSession, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.SessionReattachParams
		json.Unmarshal(raw, &p)
		mu.Lock()
		defer mu.Unlock()
		reattached = append(reattached, p.ReattachToken)
		return ipc.SharedSessionView{Name: "lead", State: "open"}, fail
	})
	d.handle(ipc.MethodLinks, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.LinksResult{Links: []ipc.LinkView{}}, nil
	})
	setFail := func(err error) { mu.Lock(); fail = err; mu.Unlock() }
	attempts := func() int { mu.Lock(); defer mu.Unlock(); return len(reattached) }
	d.start()
	cs, _ := connect(t, d, "claude-code")
	if _, isErr := callTool(t, cs, "session_share", map[string]any{"name": "lead"}); isErr {
		t.Fatal("share")
	}

	setFail(core.ErrKilled)
	d.stop()
	d.start()
	if _, isErr := callTool(t, cs, "links", nil); isErr {
		t.Fatal("links after the daemon restarted")
	}
	if n := attempts(); n != 1 {
		t.Fatalf("reattach attempts %d, want 1", n)
	}
	setFail(nil)
	callTool(t, cs, "links", nil)
	if n := attempts(); n != 2 {
		t.Fatalf("a transient failure forgot the token: %d attempts, want 2", n)
	}
	callTool(t, cs, "links", nil)
	if n := attempts(); n != 2 {
		t.Fatalf("reattached again after success: %d attempts", n)
	}

	setFail(core.ErrNotFound)
	d.stop()
	d.start()
	callTool(t, cs, "links", nil)
	d.stop()
	d.start()
	callTool(t, cs, "links", nil)
	if n := attempts(); n != 3 {
		t.Fatalf("not_found kept the token: %d attempts, want 3", n)
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

// session_share writes the wake token to a private file and hands the
// model a listener command that names the file: the token never appears in
// the model's context or on a command line.
func TestShareWritesAPrivateWakeFile(t *testing.T) {
	d := newDaemonFake(t)
	d.handle(ipc.MethodSessionShare, ipc.GateSession, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.ShareResult{Session: ipc.SharedSessionView{Name: "lead", State: "open"}, WakeToken: "WAKE-SECRET", ReattachToken: "R"}, nil
	})
	d.handle(ipc.MethodSessionClose, ipc.GateSession, func(*ipc.ConnState, json.RawMessage) (any, error) { return nil, nil })
	d.start()
	dir := filepath.Join(t.TempDir(), "wake")
	cs, sess := connectWith(t, d, "claude-code", Options{WakeDir: dir, ListenerProgram: "/opt/my tools/cravv-connect"}, nil, "")
	text, isErr := callTool(t, cs, "session_share", map[string]any{"name": "lead"})
	var out shareOut
	if err := json.Unmarshal([]byte(text), &out); isErr || err != nil || strings.Contains(text, "WAKE-SECRET") || out.WakeToken != "" || out.Next != ListenerNext {
		t.Fatalf("share output %v %q", isErr, text)
	}
	prefix := "'/opt/my tools/cravv-connect' listen --wake-file "
	path, ok := strings.CutPrefix(out.Listener, prefix)
	if !ok || filepath.Dir(path) != dir {
		t.Fatalf("listener %q", out.Listener)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("wake file %v %v", fi, err)
	}
	if di, err := os.Stat(dir); err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("wake dir %v %v", di, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "WAKE-SECRET\n" {
		t.Fatalf("wake file holds %q", b)
	}
	if _, isErr := callTool(t, cs, "session_close", nil); isErr {
		t.Fatal("close failed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("wake file left after session_close: %v", err)
	}
	callTool(t, cs, "session_share", map[string]any{"name": "lead"})
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("wake files %v", entries)
	}
	sess.Close()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("wake file left after the MCP server stopped: %v", entries)
	}
}
