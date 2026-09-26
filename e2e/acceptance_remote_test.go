package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// TestAcceptance_4_ExistingOrNew pins criterion 4, "Existing or new": "A
// host can link to a session the remote side already has open." and "A
// host can also ask the remote daemon to start a managed session in a
// folder the remote owner pre-approved. This works with no human at the
// remote machine."
//
// The GPU box's agent is the fake claude (this test binary), so the runs
// are real processes without calling Claude. Not parallel: it sets the
// fake agent's environment.
func TestAcceptance_4_ExistingOrNew(t *testing.T) {
	p := newManagedPair(t, "reply", ipc.OfferSetParams{Label: "trainer", Permission: "tasks-auto", RunMode: "edit-in-folder"})
	lead, _ := newClaudeAgent(t, p.mac, "chat-lead", nil)
	shareChat(t, lead, "lead", "private")

	// Existing: a chat the GPU box's human has open.
	wakeword := p.gpu.Share("claude", "wakeword", "all-peers")
	var listed ipc.SessionsListResult
	lead.decode("sessions", map[string]any{"machine": "gpu-box"}, &listed)
	if len(listed.Sessions) != 1 || listed.Sessions[0].Name != "wakeword" || listed.Sessions[0].Kind != "live" {
		t.Fatalf("the Mac sees sessions %+v", listed.Sessions)
	}
	if len(listed.Offers) != 1 || listed.Offers[0].Label != "trainer" || listed.Offers[0].MaxPermission != "tasks-auto" {
		t.Fatalf("the Mac sees offers %+v", listed.Offers)
	}
	var existing ipc.LinkView
	lead.decode("connect", map[string]any{"target": "gpu-box/wakeword", "permission": "messages"}, &existing)
	in := p.gpu.WaitLink(wait, "the request to wakeword", func(l ipc.LinkView) bool { return l.State == "pending" && l.Session == "wakeword" })
	p.gpu.Decide(in.Link, true, "")
	p.mac.WaitLink(wait, "linked to the open chat", func(l ipc.LinkView) bool { return l.Link == existing.Link && l.State == "active" })
	lead.call("send_message", map[string]any{"link": existing.Link, "text": "ACC4-EXISTING"})
	WaitItem(t, wakeword.C, wait, "the open chat gets it", func(it ipc.InboxView) bool { return strings.Contains(it.Wrapped, "ACC4-EXISTING") })

	// New: the offer starts a managed session and accepts the link with
	// nobody at the GPU box (no decision, no notification).
	desktopBefore, _ := p.gpu.Desktop.Last()
	var created ipc.LinkView
	lead.decode("connect", map[string]any{"target": "gpu-box/new:trainer", "permission": "tasks-auto"}, &created)
	link := p.mac.WaitLink(wait, "the managed link", func(l ipc.LinkView) bool { return l.Link == created.Link && l.State == "active" })
	if link.PermissionOut != "tasks-auto" || !strings.HasPrefix(link.RemoteSession, "trainer-") {
		t.Fatalf("managed link %+v", link)
	}
	var task ipc.TaskCreateResult
	lead.decode("create_task", map[string]any{"link": link.Link, "instructions": "ACC4-NEW count the lines"}, &task)
	Eventually(t, wait, "the managed session finished the task", func() bool {
		return strings.Contains(lead.call("get_task", map[string]any{"task_id": task.TaskID}), `"state": "done"`)
	})
	recs := p.runs(t, 1)
	if recs[0].Dir != p.folder || !strings.Contains(recs[0].Prompt, "ACC4-NEW count the lines") {
		t.Fatalf("the run %+v", recs[0])
	}
	if after, _ := p.gpu.Desktop.Last(); after != desktopBefore {
		t.Fatalf("the GPU box asked its human: %q", after)
	}

	// Only a folder the owner pre-approved: an unknown offer is not found.
	if text, isErr := lead.try("connect", map[string]any{"target": "gpu-box/new:elsewhere", "permission": "messages"}); !isErr || !strings.Contains(text, "not found") {
		t.Fatalf("connect to an unknown offer: %q", text)
	}
}

// TestAcceptance_5_OneApproval pins criterion 5, "One approval": "A link
// request is decided once, on the accepting side.", "A task under an "ask
// each time" link is decided once, on the receiving side." and "The
// sender always sees a task's state, including `seen`, so "stuck" and
// "slow" look different."
//
// The accepting chat's client shows forms (elicitation) for the link;
// for the task its human dismisses the form, as the VS Code extension
// does, and decides with the confirmation code from the desktop.
func TestAcceptance_5_OneApproval(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	human := &Human{}
	trainer, _ := newClaudeAgent(t, gpu, "chat-trainer", human)
	lead, _ := newClaudeAgent(t, mac, "chat-lead", nil)
	shareChat(t, trainer, "trainer", "all-peers")
	shareChat(t, lead, "lead", "private")

	// The link: one decision, by the accepting side's human, in a form.
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-ask", "note": "ACC5-NOTE"}, &out)
	gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	if text := lead.call("review_pending", nil); text != "Nothing is waiting for a decision." {
		t.Fatalf("the requester was asked: %q", text)
	}
	human.Answer(Choose("accept"))
	trainer.call("review_pending", nil)
	mac.WaitLink(wait, "active on both sides", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
	if n := len(human.Forms()); n != 1 {
		t.Fatalf("the accepting human saw %d forms, want 1", n)
	}
	if title, _ := mac.Desktop.Last(); title != "" {
		t.Fatalf("the requesting machine notified its human: %q", title)
	}
	trainer.call("check_inbox", nil)
	lead.call("check_inbox", nil)

	// The task: held for the receiving human, who decides once.
	var task ipc.TaskCreateResult
	lead.decode("create_task", map[string]any{"link": out.Link, "instructions": "ACC5-TASK train for one epoch"}, &task)
	state := func() string {
		var tv ipc.TaskView
		if err := json.Unmarshal([]byte(lead.call("get_task", map[string]any{"task_id": task.TaskID})), &tv); err != nil {
			t.Fatal(err)
		}
		return tv.State
	}
	Eventually(t, wait, "the sender sees awaiting_approval", func() bool { return state() == "awaiting_approval" })
	human.Answer(Dismiss("decline"))
	text := trainer.call("review_pending", nil)
	code := gpu.Desktop.Code()
	item := "task-" + task.TaskID
	if len(code) != 4 || !strings.Contains(text, item) || strings.Contains(text, code) {
		t.Fatalf("review_pending %q with code %q", text, code)
	}
	if text := trainer.call("review_pending", map[string]any{"item": item, "decision": "accept", "code": code}); !strings.Contains(text, "approved by the human") {
		t.Fatalf("the typed code %q", text)
	}
	// Approved, but its session has not read it yet: queued, not seen.
	Eventually(t, wait, "the sender sees queued", func() bool { return state() == "queued" })
	if text := trainer.call("check_inbox", nil); !strings.Contains(text, "ACC5-TASK") {
		t.Fatalf("check_inbox %q", text)
	}
	Eventually(t, wait, "the sender sees seen", func() bool { return state() == "seen" })

	// Nothing asks again: not the chat, not the human's terminal.
	if text := trainer.call("review_pending", nil); text != "Nothing is waiting for a decision." {
		t.Fatalf("asked again: %q", text)
	}
	var held ipc.ApprovalsListResult
	Call(t, gpu.Unlocked(), ipc.MethodApprovalsList, nil, &held)
	if len(held.Tasks) != 0 {
		t.Fatalf("the terminal still holds %+v", held.Tasks)
	}
	if n := len(human.Forms()); n != 2 {
		t.Fatalf("the human saw %d forms, want 2 (the link, the task)", n)
	}

	// The worker runs it; the sender sees every step.
	for _, step := range []struct{ tool, state string }{{"claim_task", "claimed"}, {"update_task", "running"}, {"complete_task", "done"}} {
		args := map[string]any{"task_id": task.TaskID}
		switch step.tool {
		case "update_task":
			args["note"] = "halfway"
		case "complete_task":
			args["result"] = "ACC5-RESULT"
		}
		trainer.call(step.tool, args)
		Eventually(t, wait, fmt.Sprintf("the sender sees %s", step.state), func() bool { return state() == step.state })
	}
}
