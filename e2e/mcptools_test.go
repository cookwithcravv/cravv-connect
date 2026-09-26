package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/mcpserver"
)

// mcpAgent is an MCP client (clientInfo "claude-code") talking to the real
// MCP server, which talks to node's daemon over its IPC socket.
type mcpAgent struct {
	t  *testing.T
	cs *mcp.ClientSession
}

func newMCPAgent(t *testing.T, n *Node) *mcpAgent {
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
	t.Cleanup(func() { cs.Close(); ss.Wait(); sess.Close() })
	return &mcpAgent{t: t, cs: cs}
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

// task calls a task tool and decodes the TaskView it returns.
func (m *mcpAgent) task(name string, args map[string]any) ipc.TaskView {
	m.t.Helper()
	var tv ipc.TaskView
	out := m.call(name, args)
	if err := json.Unmarshal([]byte(out), &tv); err != nil {
		m.t.Fatalf("%s output %q: %v", name, out, err)
	}
	return tv
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

// Every agent-facing MCP tool through the real MCP server and real daemons.
func TestMCPToolsEndToEnd(t *testing.T) {
	t.Parallel()
	r, a, b := NewPair(t, PairOptions{ATrustsB: core.TrustAutonomous, BTrustsA: core.TrustAutonomous})
	ma, mb := newMCPAgent(t, a), newMCPAgent(t, b)

	// create_task, then the worker finds it, claims, updates and completes it.
	const instr = "MCP-INSTR sort the dataset"
	var created ipc.TaskCreateResult
	if err := json.Unmarshal([]byte(ma.call("create_task", map[string]any{"to": "bob", "instructions": instr})), &created); err != nil || created.TaskID == "" {
		t.Fatalf("create_task: %+v %v", created, err)
	}
	var inbox string
	Eventually(t, wait, "task in bob's MCP inbox", func() bool {
		inbox += mb.call("check_inbox", map[string]any{})
		return strings.Contains(inbox, created.TaskID)
	})
	if !strings.Contains(inbox, "<remote_message") || !strings.Contains(inbox, instr) {
		t.Fatalf("check_inbox output %q", inbox)
	}
	onlyWrapped(t, "worker get_task", mb.call("get_task", map[string]any{"task_id": created.TaskID}), instr)
	if tv := mb.task("claim_task", map[string]any{"task_id": created.TaskID}); tv.State != "claimed" || tv.ClaimedBy != "claude@proj" {
		t.Fatalf("claim_task: %+v", tv)
	}
	if tv := mb.task("update_task", map[string]any{"task_id": created.TaskID, "note": "MCP-NOTE halfway"}); tv.State != "running" {
		t.Fatalf("update_task: %+v", tv)
	}
	if tv := mb.task("complete_task", map[string]any{"task_id": created.TaskID, "result": "MCP-RESULT sorted"}); tv.State != "done" {
		t.Fatalf("complete_task: %+v", tv)
	}
	Eventually(t, wait, "sender sees the result", func() bool {
		tv := ma.task("get_task", map[string]any{"task_id": created.TaskID})
		return tv.State == "done" && strings.Contains(tv.Wrapped, "MCP-RESULT sorted")
	})
	senderView := ma.call("get_task", map[string]any{"task_id": created.TaskID})
	onlyWrapped(t, "sender get_task (result)", senderView, "MCP-RESULT sorted")
	onlyWrapped(t, "sender get_task (note)", senderView, "MCP-NOTE halfway")

	// fail_task.
	var second ipc.TaskCreateResult
	json.Unmarshal([]byte(ma.call("create_task", map[string]any{"to": "bob", "instructions": "MCP-INSTR second"})), &second)
	Eventually(t, wait, "second task at bob", func() bool {
		_, isErr := mb.try("claim_task", map[string]any{"task_id": second.TaskID})
		return !isErr
	})
	if tv := mb.task("fail_task", map[string]any{"task_id": second.TaskID, "reason": "MCP-REASON no disk"}); tv.State != "failed" {
		t.Fatalf("fail_task: %+v", tv)
	}
	Eventually(t, wait, "sender sees the failure", func() bool {
		return ma.task("get_task", map[string]any{"task_id": second.TaskID}).State == "failed"
	})
	onlyWrapped(t, "sender get_task (failure)", ma.call("get_task", map[string]any{"task_id": second.TaskID}), "MCP-REASON no disk")

	// cancel_task.
	var third ipc.TaskCreateResult
	json.Unmarshal([]byte(ma.call("create_task", map[string]any{"to": "bob", "instructions": "MCP-INSTR third"})), &third)
	if tv := ma.task("cancel_task", map[string]any{"task_id": third.TaskID}); tv.State != "cancelled" {
		t.Fatalf("cancel_task: %+v", tv)
	}

	// send_file.
	data := []byte("MCP-FILE contents\n")
	if err := os.WriteFile(filepath.Join(a.Proj, "mcp.txt"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var sent ipc.FileSendResult
	if err := json.Unmarshal([]byte(ma.call("send_file", map[string]any{"to": "bob", "path": "mcp.txt"})), &sent); err != nil || sent.FileID == "" {
		t.Fatalf("send_file: %+v %v", sent, err)
	}
	sb, _ := b.Session("codex")
	item, _ := WaitItem(t, sb, wait, "MCP file at bob", func(it ipc.InboxView) bool {
		return it.Kind == "file" && it.FileID == sent.FileID && it.Path != ""
	})
	if got, err := os.ReadFile(item.Path); err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("received file: %v", err)
	}
	if out, isErr := ma.try("send_file", map[string]any{"to": "bob", "path": ".env"}); !isErr {
		t.Fatalf("send_file of a secret succeeded: %s", out)
	}

	// unpair_peer on a throwaway pair.
	c := NewNode(t, r, "carol", NodeOptions{})
	Pair(t, a, c, PairOptions{})
	if out := ma.call("unpair_peer", map[string]any{"alias": "carol"}); !strings.Contains(out, "Unpaired carol") {
		t.Fatalf("unpair_peer: %q", out)
	}
	if _, ok := a.PeerView("carol"); ok {
		t.Fatal("carol still paired after unpair_peer")
	}

	// pause_peer.
	if out := ma.call("pause_peer", map[string]any{"alias": "bob"}); !strings.Contains(out, "Paused bob") {
		t.Fatalf("pause_peer: %q", out)
	}
	if pv, ok := a.PeerView("bob"); !ok || !pv.Paused {
		t.Fatalf("bob after pause_peer: %+v", pv)
	}
	if out, isErr := ma.try("send_message", map[string]any{"to": "bob", "text": "x"}); !isErr {
		t.Fatalf("send_message to a paused peer succeeded: %s", out)
	}
}
