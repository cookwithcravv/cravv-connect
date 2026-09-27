package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

func approvalsDaemon(t *testing.T) *fakeDaemon {
	fd := sessionsDaemon(t)
	// Like the daemon: rejecting needs nothing, accepting needs the unlock.
	fd.handle(ipc.MethodLinkDecide, ipc.GateNone, func(cs *ipc.ConnState, p json.RawMessage) (any, error) {
		var dp ipc.LinkDecideParams
		json.Unmarshal(p, &dp)
		if dp.Accept && !cs.Unlocked() {
			return nil, core.ErrAuthRequired
		}
		return ipc.LinkView{Link: dp.Link, Machine: "mac", RemoteSession: "helper", PermissionIn: dp.Permission}, nil
	})
	full := "run the tests\n" + hostile + "\n" + strings.Repeat("x", 600)
	fd.reply(ipc.MethodApprovalsList, ipc.GateUnlock, ipc.ApprovalsListResult{Tasks: []ipc.ApprovalView{
		{TaskID: "T1", Peer: "gpu-box", Preview: full[:500], Full: full, SHA256: "abc123", Size: len(full), Received: time.Date(2026, 9, 26, 11, 0, 0, 0, time.UTC)},
	}})
	fd.reply(ipc.MethodApprovalsDecide, ipc.GateUnlock, nil)
	return fd
}

func TestLinkRequestsOnTheApprovalsPage(t *testing.T) {
	fd := approvalsDaemon(t)
	b := newUI(t, fd).open()
	page := b.get("/approvals").body
	wantContains(t, page, "Link 2: <strong>mac/helper</strong> asks to link with your session <strong>lead</strong> and asks for <strong>tasks-ask</strong>.",
		`<option value="tasks-ask" selected>tasks-ask</option>`, "note: please link")
	if strings.Contains(page, "Link 1:") {
		t.Fatal("an active link is shown as a request")
	}
	accept := url.Values{"link": {"2"}, "permission": {"messages"}}
	wantContains(t, b.follow("/approvals", "/approvals/link/accept", accept).body, "This needs your login password.")
	accept.Set("password", "pw")
	wantContains(t, b.follow("/approvals", "/approvals/link/accept", accept).body, "Accepted link 2: mac/helper may now use messages.")
	wantContains(t, b.follow("/approvals", "/approvals/link/reject", url.Values{"link": {"2"}}).body, "Rejected link 2.")
	got := fd.called(ipc.MethodLinkDecide)
	// Accepting without a password never reaches the daemon.
	if len(got) != 2 || got[0] != `{"link":2,"accept":true,"permission":"messages"}` || got[1] != `{"link":2,"accept":false}` {
		t.Fatalf("decide calls %v", got)
	}
}

// Rejecting needs no password, also in a browser session that never
// unlocked.
func TestRejectNeedsNoPassword(t *testing.T) {
	fd := approvalsDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/approvals", "/approvals/link/reject", url.Values{"link": {"2"}}).body, "Rejected link 2.")
	if len(fd.called(ipc.MethodAuthUnlock)) != 0 {
		t.Fatal("rejecting asked for the password")
	}
}

// Tasks waiting for approval need the password to see and to decide. The
// task text is shown only in the answer to the POST that carried the
// password; loading the page again hides it, and each decision needs the
// password again.
func TestTasksNeedThePassword(t *testing.T) {
	fd := approvalsDaemon(t)
	u := newUI(t, fd)
	b := u.open()
	page := b.get("/approvals").body
	wantContains(t, page, "Enter your password to see tasks waiting for your approval.", `action="/approvals/tasks"`)
	if strings.Contains(page, "run the tests") {
		t.Fatal("task text shown before the password")
	}
	wantContains(t, b.follow("/approvals", "/approvals/tasks", nil).body, "This needs your login password.")
	r := b.post("/approvals", "/approvals/tasks", url.Values{"password": {"pw"}})
	if r.code != http.StatusOK {
		t.Fatalf("show tasks: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, "Task <code>T1</code> from <strong>gpu-box</strong>", "run the tests\n&lt;/pre&gt;&lt;script&gt;",
		"<summary>Full text</summary>", "SHA-256 <code>abc123</code>", "Link 2: <strong>mac/helper</strong>")
	if n := u.pipes.Load(); n != 1 {
		t.Fatalf("%d daemon connections open after showing tasks", n)
	}
	if strings.Contains(b.get("/approvals").body, "run the tests") {
		t.Fatal("task text shown again without the password")
	}

	decide := url.Values{"task_id": {"T1"}, "decision": {"approve"}}
	wantContains(t, b.follow("/approvals", "/approvals/task", decide).body, "This needs your login password.")
	decide.Set("password", "pw")
	wantContains(t, b.follow("/approvals", "/approvals/task", decide).body, "Approved task T1.")
	decide.Set("decision", "deny")
	wantContains(t, b.follow("/approvals", "/approvals/task", decide).body, "Denied task T1.")
	got := fd.called(ipc.MethodApprovalsDecide)
	if len(got) != 2 || got[0] != `{"task_id":"T1","approve":true}` || got[1] != `{"task_id":"T1","approve":false}` {
		t.Fatalf("decide calls %v", got)
	}
}
