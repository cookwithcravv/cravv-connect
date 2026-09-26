package e2e

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

const wait = 20 * time.Second

func isChat(id string) func(ipc.InboxView) bool {
	return func(it ipc.InboxView) bool { return it.Kind == "chat" && it.ID == id }
}

// sendChat sends text on link number link of the chat shared on c.
func sendChat(t *testing.T, c *ipc.Client, link int64, text string) string {
	t.Helper()
	var r ipc.IDResult
	Call(t, c, ipc.MethodChatSend, ipc.ChatSendParams{Link: link, Text: text}, &r)
	return r.ID
}

func wantKind(t *testing.T, err error, kind string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got success, want error kind %q", kind)
	}
	if !ipc.IsKind(err, kind) {
		t.Fatalf("got error %v (kind %q), want kind %q", err, ipc.KindOf(err), kind)
	}
}

// Criteria 1 and 4: pairing needs the password on both sides; the first
// machine registers with the admin token, the second with the invite sent
// inside the encrypted pairing exchange; pairing alone grants no links.
func TestPairingNeedsPasswordAndRegistersWithInvite(t *testing.T) {
	t.Parallel()
	r := NewRelay(t)
	a := NewNode(t, r, "alice", NodeOptions{AdminToken: AdminToken})
	b := NewNode(t, r, "bob", NodeOptions{})
	a.WaitOnline()

	locked := a.Conn()
	wantKind(t, TryCall(locked, ipc.MethodPairStart, nil, nil), ipc.KindAuthRequired)
	wantKind(t, TryCall(b.Conn(), ipc.MethodJoinStart, ipc.JoinStartParams{Code: "CRAVV-0000-0000-0000"}, nil), ipc.KindAuthRequired)
	wantKind(t, TryCall(locked, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "wrong"}, nil), ipc.KindBadPassword)
	wantKind(t, TryCall(locked, ipc.MethodPairStart, nil, nil), ipc.KindAuthRequired)

	if b.Status().RelayConnected {
		t.Fatal("bob has a mailbox before pairing, want none (no token, no invite)")
	}
	Pair(t, a, b)

	if !b.Status().RelayConnected {
		t.Fatal("bob not connected after registering with the invite")
	}
	pa, ok := a.PeerView("bob")
	if !ok || pa.MachineID != string(b.Daemon.Identity().MachineID()) {
		t.Fatalf("alice sees bob as %+v (found %v)", pa, ok)
	}
	if _, ok := b.PeerView("alice"); !ok {
		t.Fatal("bob does not know alice")
	}
	// Pairing grants no links: the machines see each other, nothing more.
	if len(a.AllLinks()) != 0 || len(b.AllLinks()) != 0 {
		t.Fatal("pairing created links")
	}

	for _, n := range []*Node{a, b} {
		evs, err := audit.ReadEvents(n.Paths.Audit, 0)
		if err != nil {
			t.Fatal(err)
		}
		var pairs, badPw int
		for _, e := range evs {
			switch {
			case e.Type == audit.EvPair:
				pairs++
			case e.Type == audit.EvPassword && e.Detail["ok"] == false:
				badPw++
			}
		}
		if pairs != 1 {
			t.Errorf("%s: %d pair audit events, want 1", n.Name, pairs)
		}
		if n == a && badPw != 1 {
			t.Errorf("alice: %d failed password audit events, want 1", badPw)
		}
	}
}

// Criterion 2: chat both ways over a link, wrapped as untrusted content with
// the local alias, the peer's session name, the link number and permission.
func TestChatBothWays(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")

	id := sendChat(t, l.A.C, l.ANum, "hello from alice <b>&</b>")
	got, _ := WaitItem(t, l.B.C, wait, "chat at bob", isChat(id))
	for _, want := range []string{`from="alice"`, `session="lead"`, fmt.Sprintf(`link="%d"`, l.BNum), `permission="messages"`, "hello from alice &lt;b&gt;&amp;&lt;/b&gt;"} {
		if !strings.Contains(got.Wrapped, want) {
			t.Errorf("wrapped %q lacks %q", got.Wrapped, want)
		}
	}
	if got.Session != "lead" || got.Link != l.BNum {
		t.Fatalf("view %+v", got)
	}

	back := sendChat(t, l.B.C, l.BNum, "hi alice")
	reply, _ := WaitItem(t, l.A.C, wait, "reply at alice", isChat(back))
	if !strings.Contains(reply.Wrapped, `from="bob"`) || !strings.Contains(reply.Wrapped, `session="trainer"`) {
		t.Fatalf("reply wrapped %q", reply.Wrapped)
	}

	// Delivered receipts drain both outboxes. Control messages (the receipts
	// themselves) are never confirmed, so they must leave the outbox once the
	// relay has queued them.
	for _, n := range []*Node{a, b} {
		Eventually(t, wait, n.Name+"'s outbox drains", func() bool {
			st := n.Status()
			return st.OutboxPending == 0 && st.OutboxHeld == 0
		})
	}
}

// v2 success criterion 2: traffic on one link is never visible to another
// session, on either machine.
func TestCrossSessionIsolation(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "tasks-auto")
	other := b.Share("codex", "other", "private")

	chat := sendChat(t, l.A.C, l.ANum, "only for trainer")
	var created ipc.TaskCreateResult
	Call(t, l.A.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: "only for trainer too"}, &created)
	WaitItem(t, l.B.C, wait, "task at trainer", func(it ipc.InboxView) bool { return it.TaskID == created.TaskID })
	if items := Inbox(t, other.C); len(items) != 0 {
		t.Fatalf("another session saw %+v", items)
	}
	wantKind(t, TryCall(other.C, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, nil), ipc.KindNotFound)
	wantKind(t, TryCall(other.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil), ipc.KindNotFound)
	wantKind(t, TryCall(other.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: l.BNum, Text: "hijack"}, nil), ipc.KindNotFound)
	var mine ipc.LinksResult
	Call(t, other.C, ipc.MethodLinks, nil, &mine)
	if len(mine.Links) != 0 {
		t.Fatalf("another session lists %+v", mine.Links)
	}
	_ = chat
}

// Criterion 2: a task on a tasks-auto link runs create, claim, update,
// complete, and the sender sees every state (including seen) and the result
// through inbox.wait.
func TestTaskOverTasksAutoLink(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "tasks-auto")

	var created ipc.TaskCreateResult
	Call(t, l.A.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: "count the lines in README"}, &created)

	item, _ := WaitItem(t, l.B.C, wait, "task at bob", func(it ipc.InboxView) bool {
		return it.Kind == "task" && it.TaskID == created.TaskID
	})
	if !strings.Contains(item.Wrapped, `kind="task"`) || !strings.Contains(item.Wrapped, "count the lines in README") ||
		!strings.Contains(item.Wrapped, `permission="tasks-auto"`) {
		t.Fatalf("task item wrapped %q", item.Wrapped)
	}

	// Bob's inbox returned the task: alice sees "seen" before anyone claims it.
	var tv ipc.TaskView
	Eventually(t, wait, "sender sees seen", func() bool {
		Call(t, l.A.C, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		return tv.State == "seen"
	})
	Call(t, l.B.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
	if tv.State != "claimed" {
		t.Fatalf("after claim: %+v", tv)
	}
	Call(t, l.B.C, ipc.MethodTaskUpdate, ipc.TaskUpdateParams{TaskID: created.TaskID, Note: "halfway"}, &tv)
	if tv.State != "running" {
		t.Fatalf("after update: state %q", tv.State)
	}
	Call(t, l.B.C, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: created.TaskID, Result: "42 lines"}, &tv)
	if tv.State != "done" {
		t.Fatalf("after complete: state %q", tv.State)
	}

	// The sender listens with inbox.wait, as wait_for_message does.
	var result *ipc.InboxView
	deadline := time.Now().Add(wait)
	for result == nil && time.Now().Before(deadline) {
		var r ipc.InboxResult
		Call(t, l.A.C, ipc.MethodInboxWait, ipc.InboxWaitParams{TimeoutS: 5}, &r)
		for _, it := range r.Items {
			if it.Kind == "task_update" && it.TaskID == created.TaskID && strings.Contains(it.Wrapped, "42 lines") {
				v := it
				result = &v
			}
		}
	}
	if result == nil {
		t.Fatal("sender never saw the result through inbox.wait")
	}
	var sv ipc.TaskView
	Call(t, l.A.C, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &sv)
	// The result is peer text: only the wrapped view carries it.
	if sv.State != "done" || sv.Result != "" || !strings.Contains(sv.Wrapped, "Result:\n42 lines") ||
		!strings.Contains(sv.Wrapped, `<remote_message from="bob" session="trainer"`) || sv.Direction != "out" || sv.ClaimedBy != "trainer" {
		t.Fatalf("sender view %+v", sv)
	}
}

// v2 spec 3.4: a task on a tasks-ask link waits for the receiving human
// (password in Phase 1); a tasks-auto link queues it at once.
func TestTasksAskNeedsApprovalTasksAutoDoesNot(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "tasks-ask")

	var created ipc.TaskCreateResult
	Call(t, l.A.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: "delete the build cache"}, &created)
	Eventually(t, wait, "sender sees awaiting_approval", func() bool {
		var tv ipc.TaskView
		Call(t, l.A.C, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		return tv.State == "awaiting_approval"
	})
	if n := b.Status().PendingApprovals; n != 1 {
		t.Fatalf("bob pending approvals = %d, want 1", n)
	}
	// The agent cannot claim it, list approvals, or approve it.
	wantKind(t, TryCall(l.B.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil), ipc.KindBadTransition)
	wantKind(t, TryCall(l.B.C, ipc.MethodApprovalsList, nil, nil), ipc.KindAuthRequired)
	wantKind(t, TryCall(l.B.C, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: created.TaskID, Approve: true}, nil), ipc.KindAuthRequired)
	for _, it := range Inbox(t, l.B.C) {
		if it.TaskID == created.TaskID {
			t.Fatalf("held task reached the session: %+v", it)
		}
	}

	human := b.Unlocked()
	var list ipc.ApprovalsListResult
	Call(t, human, ipc.MethodApprovalsList, nil, &list)
	if len(list.Tasks) != 1 || list.Tasks[0].TaskID != created.TaskID || list.Tasks[0].Peer != "alice" {
		t.Fatalf("approvals %+v", list.Tasks)
	}
	sum := sha256.Sum256([]byte("delete the build cache"))
	if list.Tasks[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("approval hash %s", list.Tasks[0].SHA256)
	}
	Call(t, human, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: created.TaskID, Approve: true}, nil)
	WaitItem(t, l.B.C, wait, "approved task delivered", func(it ipc.InboxView) bool {
		return it.Kind == "task" && it.TaskID == created.TaskID
	})
	var tv ipc.TaskView
	Call(t, l.B.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
	Eventually(t, wait, "sender sees claimed", func() bool {
		Call(t, l.A.C, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		return tv.State == "claimed"
	})

	// The same peer on a tasks-auto link needs no approval.
	auto := LinkChats(t, a, b, a.Share("codex", "lead-2", "private"), b.Share("codex", "worker", "all-peers"), "tasks-auto")
	var quick ipc.TaskCreateResult
	Call(t, auto.A.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: auto.ANum, Instructions: "quick"}, &quick)
	WaitItem(t, auto.B.C, wait, "tasks-auto task delivered", func(it ipc.InboxView) bool { return it.TaskID == quick.TaskID })
	if n := b.Status().PendingApprovals; n != 0 {
		t.Fatalf("pending approvals %d after a tasks-auto task", n)
	}
}

// Lowering a link (restrict) needs no password and reaches the peer: the
// peer can no longer create tasks there. Raising back needs the password.
func TestRestrictLowersAndRaisingNeedsPassword(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "tasks-auto")
	var v ipc.LinkView
	Call(t, l.B.C, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: l.BNum, Permission: "messages"}, &v)
	if v.PermissionIn != "messages" {
		t.Fatalf("restricted link %+v", v)
	}
	a.WaitLink(wait, "alice learns the lower permission", func(x ipc.LinkView) bool {
		return x.Link == l.ANum && x.PermissionOut == "messages"
	})
	wantKind(t, TryCall(l.A.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: "x"}, nil), ipc.KindNotPermitted)
	id := sendChat(t, l.A.C, l.ANum, "chat still works")
	WaitItem(t, l.B.C, wait, "chat after restrict", isChat(id))

	wantKind(t, TryCall(l.B.C, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: l.BNum, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
	wantKind(t, TryCall(b.Conn(), ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: l.BNum, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
	Call(t, b.Unlocked(), ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: l.BNum, Permission: "tasks-auto"}, nil)
	a.WaitLink(wait, "alice learns the raise", func(x ipc.LinkView) bool {
		return x.Link == l.ANum && x.PermissionOut == "tasks-auto"
	})
}

// Criterion 2 and spec 7.4: files arrive intact under files/<alias>/ over a
// messages link, and secrets never leave.
func TestFileTransfer(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")

	data := make([]byte, 3*core.FileChunkBytes+12345)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(a.Proj, "model.bin")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var sent ipc.FileSendResult
	Call(t, l.A.C, ipc.MethodFileSend, ipc.FileSendParams{Link: l.ANum, Path: "model.bin"}, &sent)

	item, _ := WaitItem(t, l.B.C, 60*time.Second, "file at bob", func(it ipc.InboxView) bool {
		return it.Kind == "file" && it.FileID == sent.FileID && it.Path != ""
	})
	wantDir := filepath.Join(b.Paths.Files, "alice") + string(filepath.Separator)
	if !strings.HasPrefix(item.Path, wantDir) || !strings.HasSuffix(item.Path, "-model.bin") || item.Link != l.BNum {
		t.Fatalf("saved at %q (link %d), want %s<msgid>-model.bin", item.Path, item.Link, wantDir)
	}
	got, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatal("received file differs from the original")
	}

	// Outbound limits: secrets, dot-directories and paths outside the project.
	secrets := map[string]string{
		".env":             "TOKEN=1",
		"id_ed25519":       "key",
		"server.pem":       "pem",
		".git/config":      "cfg",
		"../../store.db.x": "outside",
	}
	for name, body := range secrets {
		p := filepath.Join(a.Proj, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		wantKind(t, TryCall(l.A.C, ipc.MethodFileSend, ipc.FileSendParams{Link: l.ANum, Path: name}, nil), ipc.KindPathRefused)
	}
}

// Criterion 5 and v2 spec 3.4: a pause by either side closes every link with
// the machine at once; sends fail with link_closed; after resume the
// sessions link again.
func TestPauseClosesLinksAndResumeAllowsNewOnes(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")

	Call(t, l.B.C, ipc.MethodPeerPause, ipc.AliasParams{Alias: "alice"}, nil)
	if v := b.Link(l.BNum); v.State != "closed" || v.Reason != core.ClosePaused {
		t.Fatalf("bob's link after his pause: %+v", v)
	}
	a.WaitLink(wait, "alice's link closes", func(v ipc.LinkView) bool {
		return v.Link == l.ANum && v.State == "closed" && v.Reason == core.ClosePaused
	})
	Eventually(t, wait, "alice learns she is paused", func() bool {
		p, _ := a.PeerView("bob")
		return p.PausedByPeer && !p.Online
	})
	wantKind(t, TryCall(l.A.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: l.ANum, Text: "x"}, nil), ipc.KindLinkClosed)
	wantKind(t, TryCall(l.B.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: l.BNum, Text: "x"}, nil), ipc.KindLinkClosed)
	wantKind(t, TryCall(l.A.C, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "bob/trainer", Permission: "messages"}, nil), ipc.KindPausedByPeer)

	Call(t, b.Conn(), ipc.MethodPeerResume, ipc.AliasParams{Alias: "alice"}, nil)
	Eventually(t, wait, "alice sees bob again", func() bool {
		p, _ := a.PeerView("bob")
		return !p.PausedByPeer
	})
	again := LinkChats(t, a, b, l.A, l.B, "messages")
	id := sendChat(t, again.B.C, again.BNum, "resumed")
	WaitItem(t, again.A.C, wait, "chat on the new link", isChat(id))
}

// Criterion 5: unpair removes the peer on both sides and closes the links.
func TestUnpair(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")

	Call(t, l.A.C, ipc.MethodPeerUnpair, ipc.AliasParams{Alias: "bob"}, nil)
	if _, ok := a.PeerView("bob"); ok {
		t.Fatal("alice still lists bob")
	}
	Eventually(t, wait, "bob drops alice", func() bool {
		_, ok := b.PeerView("alice")
		return !ok
	})
	if v := a.Link(l.ANum); v.State != "closed" || v.Reason != core.CloseUnpaired {
		t.Fatalf("alice's link after unpair: %+v", v)
	}
	b.WaitLink(wait, "bob's link closes", func(v ipc.LinkView) bool { return v.Link == l.BNum && v.State == "closed" })
	wantKind(t, TryCall(l.A.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: l.ANum, Text: "x"}, nil), ipc.KindLinkClosed)
	wantKind(t, TryCall(l.B.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: l.BNum, Text: "x"}, nil), ipc.KindLinkClosed)
}

// Criterion 5 and spec 10: the kill switch stops everything but status and
// resume, survives a restart, fails claimed tasks, closes every link, and
// resume needs the password.
func TestKillSwitch(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "tasks-auto")

	var created ipc.TaskCreateResult
	Call(t, l.A.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: "long job"}, &created)
	WaitItem(t, l.B.C, wait, "task at bob", func(it ipc.InboxView) bool { return it.TaskID == created.TaskID })
	Call(t, l.B.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil)

	Call(t, l.B.C, ipc.MethodKill, nil, nil) // an agent may pull it: no password
	for _, m := range []struct {
		method string
		params any
	}{
		{ipc.MethodChatSend, ipc.ChatSendParams{Link: l.BNum, Text: "x"}},
		{ipc.MethodInboxCheck, ipc.InboxCheckParams{}},
		{ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}},
		{ipc.MethodPeerPause, ipc.AliasParams{Alias: "alice"}},
	} {
		wantKind(t, TryCall(l.B.C, m.method, m.params, nil), ipc.KindKilled)
	}
	if st := b.Status(); !st.Killed || st.RelayConnected {
		t.Fatalf("status while killed: killed=%v relay=%v", st.Killed, st.RelayConnected)
	}
	a.WaitLink(wait, "alice's link closed by the kill", func(v ipc.LinkView) bool {
		return v.Link == l.ANum && v.State == "closed" && v.Reason == core.CloseKilled
	})
	Eventually(t, wait, "sender sees failed(killed)", func() bool {
		var tv ipc.TaskView
		Call(t, l.A.C, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		// The peer's note is peer text: it arrives only inside the wrapper.
		return tv.State == "failed" && strings.Contains(tv.Wrapped, " killed\n") && len(tv.Notes) == 0
	})

	b.Restart()
	if !b.Status().Killed {
		t.Fatal("kill switch did not survive a restart")
	}
	wantKind(t, TryCall(b.Conn(), ipc.MethodResume, nil, nil), ipc.KindAuthRequired)
	Call(t, b.Unlocked(), ipc.MethodResume, nil, nil)
	b.WaitOnline()

	sb2 := b.Reattach("claude", l.B)
	again := LinkChats(t, a, b, l.A, sb2, "messages")
	id := sendChat(t, again.A.C, again.ANum, "after resume")
	WaitItem(t, sb2.C, wait, "chat after resume", isChat(id))
}

// Spec 11: with the relay down, sends stay in the outbox; after the relay
// comes back on the same address they are delivered and confirmed.
func TestRelayOfflineThenRestart(t *testing.T) {
	t.Parallel()
	r, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")

	r.Stop()
	Eventually(t, wait, "alice notices the relay is gone", func() bool { return !a.Status().RelayConnected })
	id := sendChat(t, l.A.C, l.ANum, "queued while offline")
	if st := a.Status(); st.OutboxPending < 1 {
		t.Fatalf("outbox pending = %d while offline, want >= 1", st.OutboxPending)
	}

	r.Start()
	a.WaitOnline()
	b.WaitOnline()
	WaitItem(t, l.B.C, wait, "message after relay restart", isChat(id))

	outbox := a.Daemon.Settings().(store.OutboxStore)
	Eventually(t, wait, "delivered receipt clears alice's outbox", func() bool {
		_, err := outbox.Get(context.Background(), id)
		return errors.Is(err, core.ErrNotFound)
	})
}

// Review Focus 3 (v1): the relay may deliver the same id twice (a resend
// after a lost sent reply); the receiver shows it once.
func TestDuplicateDeliveryShownOnce(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")
	ctx := context.Background()

	bob, _, err := a.Daemon.Peers().Resolve(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	link, err := a.Daemon.Links().Get(ctx, l.ANum)
	if err != nil {
		t.Fatal(err)
	}
	env, err := core.NewEnvelope(a.Clock, a.Daemon.Identity().MachineID(), bob.MachineID, core.KindChat, core.ChatBody{Text: "sent twice"})
	if err != nil {
		t.Fatal(err)
	}
	env.LinkID = link.ID
	frame, err := sealing.Seal(a.Daemon.Identity(), keys.SignedPrekeyFromWire(bob.Prekey), env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mb, ok := a.Daemon.Mailbox()
	if !ok {
		t.Fatal("alice offline")
	}
	for i := range 2 {
		st, err := mb.Send(ctx, bob.MachineID, env.ID, raw)
		if err != nil || st != transport.SendQueued {
			t.Fatalf("send %d: %v %v", i, st, err)
		}
	}
	// The relay delivers in seq order, so once this later message is in,
	// both copies have been processed.
	after := sendChat(t, l.A.C, l.ANum, "after the duplicates")
	_, seen := WaitItem(t, l.B.C, wait, "later message", isChat(after))
	count := 0
	for _, it := range seen {
		if it.ID == env.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate shown %d times, want 1", count)
	}
}

// Review Focus 1 (v1): bob rotates his prekey and purges the old private
// key while alice cannot learn the new one (bob has paused her, so the
// broadcast skips her). Alice's link request, held while paused, is then
// sealed to the deleted prekey; bob answers with control.stale_prekey, alice
// re-seals, and the request arrives exactly once.
func TestStalePrekeyResend(t *testing.T) {
	t.Parallel()
	clock := core.NewFakeClock(time.Now())
	_, a, b := NewPairWithClock(t, clock)
	lead := a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	ctx := context.Background()
	trainer, err := b.Daemon.Shared().List(ctx, core.SessionOpen)
	if err != nil || len(trainer) != 1 {
		t.Fatalf("bob's sessions %+v, %v", trainer, err)
	}
	leadRec, err := a.Daemon.Shared().List(ctx, core.SessionOpen)
	if err != nil || len(leadRec) != 1 {
		t.Fatalf("alice's sessions %+v, %v", leadRec, err)
	}

	bobAtAlice := func() core.SignedPrekeyWire {
		p, _, err := a.Daemon.Peers().Resolve(ctx, "bob")
		if err != nil {
			t.Fatal(err)
		}
		return p.Prekey
	}
	old := bobAtAlice().ID

	Call(t, b.Conn(), ipc.MethodPeerPause, ipc.AliasParams{Alias: "alice"}, nil)
	Eventually(t, wait, "alice learns she is paused", func() bool {
		p, _ := a.PeerView("bob")
		return p.PausedByPeer
	})

	// 8 days: first rotation. 22 more days: second rotation, and the first
	// prekey has been superseded for more than 21 days, so it is purged.
	clock.Advance(8 * 24 * time.Hour)
	if err := b.Daemon.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	clock.Advance(22 * 24 * time.Hour)
	if err := b.Daemon.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Daemon.Settings().(store.PrekeyStore).GetPrekey(ctx, old); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old prekey still held by bob: %v", err)
	}
	if got := bobAtAlice().ID; got != old {
		t.Fatalf("alice learned the new prekey early (%s): the test would not exercise stale_prekey", got)
	}

	// Discovery refuses while paused, so the request is queued directly.
	linkID := core.NewID()
	bobID := b.Daemon.Identity().MachineID()
	body := core.LinkRequestBody{LinkID: linkID, FromSession: core.SessionRef{ID: leadRec[0].ID, Name: "lead"},
		ToSessionID: trainer[0].ID, ProposedPermission: core.PermMessages}
	if _, err := a.Daemon.Outbound().SendEnvelope(ctx, bobID, core.KindLinkRequest, "", body); err != nil {
		t.Fatal(err)
	}
	Eventually(t, wait, "request held while paused", func() bool { return a.Status().OutboxHeld >= 1 })
	Call(t, b.Conn(), ipc.MethodPeerResume, ipc.AliasParams{Alias: "alice"}, nil)

	b.WaitLink(wait, "request after the stale_prekey round trip", func(v ipc.LinkView) bool {
		return v.State == "pending" && v.Direction == "in" && v.RemoteSession == "lead"
	})
	if got := bobAtAlice().ID; got == old {
		t.Fatal("alice still holds the purged prekey after delivery")
	}
	var n int
	for _, v := range b.AllLinks() {
		if v.Direction == "in" && v.RemoteSession == "lead" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the re-sealed request arrived %d times", n)
	}
	_ = lead
}

// sendRaw seals env from a to its recipient and posts it straight to the
// relay, bypassing a's outbox (which refuses what a v2 machine never sends).
func sendRaw(t *testing.T, a *Node, env core.Envelope) {
	t.Helper()
	ctx := context.Background()
	to, _, err := a.Daemon.Peers().Resolve(ctx, string(env.ToMachine))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := sealing.Seal(a.Daemon.Identity(), keys.SignedPrekeyFromWire(to.Prekey), env)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := frame.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mb, ok := a.Daemon.Mailbox()
	if !ok {
		t.Fatal(a.Name + " offline")
	}
	if st, err := mb.Send(ctx, to.MachineID, env.ID, raw); err != nil || st != transport.SendQueued {
		t.Fatalf("send: %v %v", st, err)
	}
}

// sendAsV1 sends a link-scoped kind from a to b without a link_id, the way a
// v1 peer does.
func sendAsV1(t *testing.T, a, b *Node, kind core.Kind, body any) {
	t.Helper()
	env, err := core.NewEnvelope(a.Clock, a.Daemon.Identity().MachineID(), b.Daemon.Identity().MachineID(), kind, body)
	if err != nil {
		t.Fatal(err)
	}
	sendRaw(t, a, env)
}

func olderNotice(n *Node, alias string) bool {
	for _, e := range n.Status().Errors {
		if strings.Contains(e, alias+" runs an older cravv-connect") {
			return true
		}
	}
	return false
}

// v1 peers send chat, task.* and file.offer without a link_id. A v2 machine
// drops them and answers control.unsupported (at most once an hour); both
// humans see why in status. Once valid link traffic arrives from the peer
// (it upgraded), the "older" notice goes away.
func TestLinklessV1TrafficGetsControlUnsupported(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	trainer := b.Share("claude", "trainer", "all-peers")
	for i := range 3 {
		sendAsV1(t, a, b, core.KindChat, core.ChatBody{Text: fmt.Sprintf("v1 chat %d", i)})
	}
	sendAsV1(t, a, b, core.KindTaskCreate, core.TaskCreateBody{TaskID: core.NewID(), Instructions: "v1 task"})
	Eventually(t, wait, "alice is told bob needs protocol 2", func() bool {
		for _, e := range a.Status().Errors {
			if strings.Contains(e, "bob needs cravv-connect protocol 2") {
				return true
			}
		}
		return false
	})
	if !olderNotice(b, "alice") {
		t.Fatalf("bob's status errors %v", b.Status().Errors)
	}
	if items := Inbox(t, trainer.C); len(items) != 0 {
		t.Fatalf("link-less traffic reached a session: %+v", items)
	}
	if st := b.Status(); st.PendingApprovals != 0 {
		t.Fatal("a link-less task is waiting for approval")
	}

	lead := a.Share("claude", "lead", "private")
	l := LinkChats(t, a, b, lead, trainer, "messages")
	id := sendChat(t, l.A.C, l.ANum, "upgraded")
	WaitItem(t, l.B.C, wait, "chat on the link", isChat(id))
	if olderNotice(b, "alice") {
		t.Fatalf("bob still says alice is older after link traffic: %v", b.Status().Errors)
	}
}
