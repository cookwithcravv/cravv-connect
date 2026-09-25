package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/mcpserver"
	"github.com/cravv/cravv-connect/internal/sealing"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

const wait = 20 * time.Second

func isChat(id string) func(ipc.InboxView) bool {
	return func(it ipc.InboxView) bool { return it.Kind == "chat" && it.ID == id }
}

func sendChat(t *testing.T, c *ipc.Client, to, text string) string {
	t.Helper()
	var r ipc.IDResult
	Call(t, c, ipc.MethodChatSend, ipc.ChatSendParams{To: to, Text: text}, &r)
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
// inside the encrypted pairing exchange; raising trust needs the password.
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
	Pair(t, a, b, PairOptions{ATrustsB: core.TrustAskFirst, BTrustsA: core.TrustChatOnly})

	if !b.Status().RelayConnected {
		t.Fatal("bob not connected after registering with the invite")
	}
	pa, ok := a.PeerView("bob")
	if !ok || pa.TrustIn != "ask-first" || pa.MachineID != string(b.Daemon.Identity().MachineID()) {
		t.Fatalf("alice sees bob as %+v (found %v)", pa, ok)
	}
	pb, ok := b.PeerView("alice")
	if !ok || pb.TrustIn != "chat-only" {
		t.Fatalf("bob sees alice as %+v (found %v)", pb, ok)
	}

	// Raising trust needs the password; lowering does not.
	plain := b.Conn()
	wantKind(t, TryCall(plain, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "alice", Level: "autonomous"}, nil), ipc.KindAuthRequired)
	Call(t, b.Unlocked(), ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "alice", Level: "autonomous"}, nil)
	Call(t, plain, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "alice", Level: "ask-first"}, nil)
	if pb, _ := b.PeerView("alice"); pb.TrustIn != "ask-first" {
		t.Fatalf("trust after lowering = %q, want ask-first", pb.TrustIn)
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

// Criterion 2: chat both ways, wrapped as untrusted content with local aliases.
func TestChatBothWays(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{ATrustsB: core.TrustAutonomous, BTrustsA: core.TrustAskFirst})
	sa, _ := a.Session("claude")
	sb, nameB := b.Session("codex")
	if nameB != "codex@proj" {
		t.Fatalf("session name %q, want codex@proj", nameB)
	}

	id := sendChat(t, sa, "bob", "hello from alice <b>&</b>")
	got, _ := WaitItem(t, sb, wait, "chat at bob", isChat(id))
	for _, want := range []string{`from="alice"`, `trust="ask-first"`, `session="claude@proj"`, "hello from alice &lt;b&gt;&amp;&lt;/b&gt;"} {
		if !strings.Contains(got.Wrapped, want) {
			t.Errorf("wrapped %q lacks %q", got.Wrapped, want)
		}
	}

	back := sendChat(t, sb, "alice", "hi alice")
	reply, _ := WaitItem(t, sa, wait, "reply at alice", isChat(back))
	if !strings.Contains(reply.Wrapped, `from="bob"`) || !strings.Contains(reply.Wrapped, `trust="autonomous"`) {
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

// Addressing alias/session reaches only that session.
func TestSessionTargetedChat(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{})
	sa, _ := a.Session("claude")
	s1, n1 := b.Session("claude")
	s2, n2 := b.Session("claude")
	if n1 != "claude@proj" || n2 != "claude@proj-2" {
		t.Fatalf("session names %q, %q", n1, n2)
	}

	targeted := sendChat(t, sa, "bob/claude@proj-2", "only for the second session")
	everyone := sendChat(t, sa, "bob", "for every session")

	WaitItem(t, s2, wait, "targeted chat in claude@proj-2", isChat(targeted))
	_, seen := WaitItem(t, s1, wait, "machine-wide chat in claude@proj", isChat(everyone))
	for _, it := range seen {
		if it.ID == targeted {
			t.Fatal("message for claude@proj-2 reached claude@proj")
		}
	}
}

// Criterion 2: an autonomous peer's task runs create, claim, update,
// complete, and the sender sees the result through inbox.wait.
func TestAutonomousTaskLifecycle(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{BTrustsA: core.TrustAutonomous})
	sa, _ := a.Session("claude")
	worker, workerName := b.Session("codex")
	other, _ := b.Session("codex")

	var created ipc.TaskCreateResult
	Call(t, sa, ipc.MethodTaskCreate, ipc.TaskCreateParams{To: "bob", Instructions: "count the lines in README"}, &created)

	item, _ := WaitItem(t, worker, wait, "task at bob", func(it ipc.InboxView) bool {
		return it.Kind == "task" && it.TaskID == created.TaskID
	})
	if !strings.Contains(item.Wrapped, `kind="task"`) || !strings.Contains(item.Wrapped, "count the lines in README") {
		t.Fatalf("task item wrapped %q", item.Wrapped)
	}

	var tv ipc.TaskView
	Call(t, worker, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
	if tv.State != "claimed" || tv.ClaimedBy != workerName {
		t.Fatalf("after claim: %+v", tv)
	}
	wantKind(t, TryCall(other, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil), ipc.KindAlreadyClaimed)

	Call(t, worker, ipc.MethodTaskUpdate, ipc.TaskUpdateParams{TaskID: created.TaskID, Note: "halfway"}, &tv)
	if tv.State != "running" {
		t.Fatalf("after update: state %q", tv.State)
	}
	Call(t, worker, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: created.TaskID, Result: "42 lines"}, &tv)
	if tv.State != "done" {
		t.Fatalf("after complete: state %q", tv.State)
	}

	// The sender listens with inbox.wait, as wait_for_message does.
	var result *ipc.InboxView
	deadline := time.Now().Add(wait)
	for result == nil && time.Now().Before(deadline) {
		var r ipc.InboxResult
		Call(t, sa, ipc.MethodInboxWait, ipc.InboxWaitParams{TimeoutS: 5}, &r)
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
	Call(t, sa, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &sv)
	// The result is peer text: only the wrapped view carries it.
	if sv.State != "done" || sv.Result != "" || !strings.Contains(sv.Wrapped, "Result:\n42 lines") ||
		!strings.Contains(sv.Wrapped, `<remote_message from="bob"`) || sv.Direction != "out" {
		t.Fatalf("sender view %+v", sv)
	}
}

// Criteria 3 and 4: an ask-first task waits for approval, which needs the password.
func TestAskFirstTaskNeedsApproval(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{BTrustsA: core.TrustAskFirst})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	var created ipc.TaskCreateResult
	Call(t, sa, ipc.MethodTaskCreate, ipc.TaskCreateParams{To: "bob", Instructions: "delete the build cache"}, &created)

	Eventually(t, wait, "sender sees awaiting_approval", func() bool {
		var tv ipc.TaskView
		Call(t, sa, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		return tv.State == "awaiting_approval"
	})
	if n := b.Status().PendingApprovals; n != 1 {
		t.Fatalf("bob pending approvals = %d, want 1", n)
	}
	// The agent cannot claim it, list approvals, or approve it.
	wantKind(t, TryCall(sb, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil), ipc.KindBadTransition)
	wantKind(t, TryCall(sb, ipc.MethodApprovalsList, nil, nil), ipc.KindAuthRequired)
	wantKind(t, TryCall(sb, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: created.TaskID, Approve: true}, nil), ipc.KindAuthRequired)
	if items := Inbox(t, sb); len(items) != 0 {
		t.Fatalf("held task reached the session: %+v", items)
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

	WaitItem(t, sb, wait, "approved task delivered", func(it ipc.InboxView) bool {
		return it.Kind == "task" && it.TaskID == created.TaskID
	})
	var tv ipc.TaskView
	Call(t, sb, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
	if tv.State != "claimed" {
		t.Fatalf("claim after approval: %+v", tv)
	}
	Eventually(t, wait, "sender sees claimed", func() bool {
		Call(t, sa, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		return tv.State == "claimed"
	})
}

// Criterion 3: a chat-only peer's task is always rejected.
func TestChatOnlyTaskRejected(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{BTrustsA: core.TrustChatOnly})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	var created ipc.TaskCreateResult
	Call(t, sa, ipc.MethodTaskCreate, ipc.TaskCreateParams{To: "bob", Instructions: "run rm -rf"}, &created)
	upd, _ := WaitItem(t, sa, wait, "rejection at sender", func(it ipc.InboxView) bool {
		return it.Kind == "task_update" && it.TaskID == created.TaskID
	})
	if !strings.Contains(upd.Wrapped, "state: rejected") || !strings.Contains(upd.Wrapped, "not permitted") {
		t.Fatalf("update wrapped %q", upd.Wrapped)
	}
	var tv ipc.TaskView
	Call(t, sa, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
	if tv.State != "rejected" {
		t.Fatalf("sender state %q, want rejected", tv.State)
	}
	if n := b.Status().PendingApprovals; n != 0 {
		t.Fatalf("pending approvals %d, want 0", n)
	}
	if items := Inbox(t, sb); len(items) != 0 {
		t.Fatalf("rejected task reached bob's session: %+v", items)
	}
	wantKind(t, TryCall(sb, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil), ipc.KindBadTransition)
}

// Criterion 2 and spec 7.4: files arrive intact under files/<alias>/, secrets
// never leave, and a chat-only peer's file waits for a password-gated accept.
func TestFileTransfer(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{BTrustsA: core.TrustAskFirst})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	data := make([]byte, 3*core.FileChunkBytes+12345)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(a.Proj, "model.bin")
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var sent ipc.FileSendResult
	Call(t, sa, ipc.MethodFileSend, ipc.FileSendParams{To: "bob", Path: "model.bin"}, &sent)

	item, _ := WaitItem(t, sb, 60*time.Second, "file at bob", func(it ipc.InboxView) bool {
		return it.Kind == "file" && it.FileID == sent.FileID && it.Path != ""
	})
	wantDir := filepath.Join(b.Paths.Files, "alice") + string(filepath.Separator)
	if !strings.HasPrefix(item.Path, wantDir) || !strings.HasSuffix(item.Path, "-model.bin") {
		t.Fatalf("saved at %q, want %s<msgid>-model.bin", item.Path, wantDir)
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
		wantKind(t, TryCall(sa, ipc.MethodFileSend, ipc.FileSendParams{To: "bob", Path: name}, nil), ipc.KindPathRefused)
	}

	// Chat-only: the file is held until a human accepts it with the password.
	Call(t, b.Conn(), ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "alice", Level: "chat-only"}, nil)
	small := filepath.Join(a.Proj, "notes.txt")
	if err := os.WriteFile(small, []byte("held until accepted"), 0o600); err != nil {
		t.Fatal(err)
	}
	var held ipc.FileSendResult
	Call(t, sa, ipc.MethodFileSend, ipc.FileSendParams{To: "bob", Path: small}, &held)
	WaitItem(t, sb, wait, "held file notice", func(it ipc.InboxView) bool {
		return it.Kind == "file" && it.FileID == held.FileID && strings.Contains(it.Wrapped, "held")
	})
	wantKind(t, TryCall(b.Conn(), ipc.MethodFilesAccept, ipc.FileIDParams{FileID: held.FileID}, nil), ipc.KindAuthRequired)
	Call(t, b.Unlocked(), ipc.MethodFilesAccept, ipc.FileIDParams{FileID: held.FileID}, nil)
	Eventually(t, wait, "accepted file downloaded", func() bool {
		var fl ipc.FilesListResult
		Call(t, b.Conn(), ipc.MethodFilesList, nil, &fl)
		for _, f := range fl.Files {
			if f.FileID == held.FileID && f.State == "done" {
				body, err := os.ReadFile(f.Path)
				return err == nil && string(body) == "held until accepted"
			}
		}
		return false
	})
}

// Criterion 5: pause blocks the pauser's sends, holds the paused side's
// messages, and delivers them after resume.
func TestPauseAndResume(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	Call(t, sb, ipc.MethodPeerPause, ipc.AliasParams{Alias: "alice"}, nil)
	wantKind(t, TryCall(sb, ipc.MethodChatSend, ipc.ChatSendParams{To: "alice", Text: "x"}, nil), ipc.KindPaused)
	Eventually(t, wait, "alice learns she is paused", func() bool {
		p, _ := a.PeerView("bob")
		return p.PausedByPeer && !p.Online
	})

	id := sendChat(t, sa, "bob", "sent while paused")
	Eventually(t, wait, "message held at alice", func() bool { return a.Status().OutboxHeld >= 1 })
	if items := Inbox(t, sb); len(items) != 0 {
		t.Fatalf("paused peer's message delivered: %+v", items)
	}

	Call(t, b.Conn(), ipc.MethodPeerResume, ipc.AliasParams{Alias: "alice"}, nil)
	WaitItem(t, sb, wait, "held message after resume", isChat(id))
	Eventually(t, wait, "alice sees bob again", func() bool {
		p, _ := a.PeerView("bob")
		return !p.PausedByPeer
	})
	back := sendChat(t, sb, "alice", "resumed")
	WaitItem(t, sa, wait, "chat after resume", isChat(back))
}

// Criterion 5: unpair removes the peer on both sides.
func TestUnpair(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	Call(t, sa, ipc.MethodPeerUnpair, ipc.AliasParams{Alias: "bob"}, nil)
	if _, ok := a.PeerView("bob"); ok {
		t.Fatal("alice still lists bob")
	}
	Eventually(t, wait, "bob drops alice", func() bool {
		_, ok := b.PeerView("alice")
		return !ok
	})
	wantKind(t, TryCall(sa, ipc.MethodChatSend, ipc.ChatSendParams{To: "bob", Text: "x"}, nil), ipc.KindNotFound)
	wantKind(t, TryCall(sb, ipc.MethodChatSend, ipc.ChatSendParams{To: "alice", Text: "x"}, nil), ipc.KindNotFound)
}

// Criterion 5 and spec 10: the kill switch stops everything but status and
// resume, survives a restart, fails claimed tasks, and resume needs the password.
func TestKillSwitch(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{BTrustsA: core.TrustAutonomous})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	var created ipc.TaskCreateResult
	Call(t, sa, ipc.MethodTaskCreate, ipc.TaskCreateParams{To: "bob", Instructions: "long job"}, &created)
	WaitItem(t, sb, wait, "task at bob", func(it ipc.InboxView) bool { return it.TaskID == created.TaskID })
	Call(t, sb, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil)

	Call(t, sb, ipc.MethodKill, nil, nil) // an agent may pull it: no password
	for _, m := range []struct {
		method string
		params any
	}{
		{ipc.MethodChatSend, ipc.ChatSendParams{To: "alice", Text: "x"}},
		{ipc.MethodInboxCheck, ipc.InboxCheckParams{}},
		{ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}},
		{ipc.MethodPeerPause, ipc.AliasParams{Alias: "alice"}},
	} {
		wantKind(t, TryCall(sb, m.method, m.params, nil), ipc.KindKilled)
	}
	if st := b.Status(); !st.Killed || st.RelayConnected {
		t.Fatalf("status while killed: killed=%v relay=%v", st.Killed, st.RelayConnected)
	}

	b.Restart()
	if !b.Status().Killed {
		t.Fatal("kill switch did not survive a restart")
	}
	wantKind(t, TryCall(b.Conn(), ipc.MethodResume, nil, nil), ipc.KindAuthRequired)
	Call(t, b.Unlocked(), ipc.MethodResume, nil, nil)
	b.WaitOnline()

	Eventually(t, wait, "sender sees failed(killed)", func() bool {
		var tv ipc.TaskView
		Call(t, sa, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: created.TaskID}, &tv)
		// The peer's note is peer text: it arrives only inside the wrapper.
		return tv.State == "failed" && strings.Contains(tv.Wrapped, " killed\n") && len(tv.Notes) == 0
	})
	sb2, _ := b.Session("codex")
	id := sendChat(t, sa, "bob", "after resume")
	WaitItem(t, sb2, wait, "chat after resume", isChat(id))
}

// Spec 11: with the relay down, sends stay in the outbox; after the relay
// comes back on the same address they are delivered and confirmed.
func TestRelayOfflineThenRestart(t *testing.T) {
	t.Parallel()
	r, a, b := NewPair(t, PairOptions{})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")

	r.Stop()
	Eventually(t, wait, "alice notices the relay is gone", func() bool { return !a.Status().RelayConnected })
	id := sendChat(t, sa, "bob", "queued while offline")
	if st := a.Status(); st.OutboxPending < 1 {
		t.Fatalf("outbox pending = %d while offline, want >= 1", st.OutboxPending)
	}

	r.Start()
	a.WaitOnline()
	b.WaitOnline()
	WaitItem(t, sb, wait, "message after relay restart", isChat(id))

	outbox := a.Daemon.Settings().(store.OutboxStore)
	Eventually(t, wait, "delivered receipt clears alice's outbox", func() bool {
		_, err := outbox.Get(context.Background(), id)
		return errors.Is(err, core.ErrNotFound)
	})
}

// Review Focus 3: the relay may deliver the same id twice (a resend after a
// lost sent reply); the receiver shows it once.
func TestDuplicateDeliveryShownOnce(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{})
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")
	ctx := context.Background()

	bob, _, err := a.Daemon.Peers().Resolve(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	env, err := core.NewEnvelope(a.Clock, a.Daemon.Identity().MachineID(), bob.MachineID, core.KindChat, core.ChatBody{Text: "sent twice"})
	if err != nil {
		t.Fatal(err)
	}
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
	after := sendChat(t, sa, "bob", "after the duplicates")
	_, seen := WaitItem(t, sb, wait, "later message", isChat(after))
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

// Review Focus 1: bob rotates his prekey and purges the old private key
// while alice cannot learn the new one (bob has paused her, so the broadcast
// skips her). Alice then seals to the deleted prekey; bob answers with
// control.stale_prekey, alice re-seals, and the message arrives exactly once.
func TestStalePrekeyResend(t *testing.T) {
	t.Parallel()
	clock := core.NewFakeClock(time.Now())
	_, a, b := NewPairWithClock(t, PairOptions{}, clock)
	sa, _ := a.Session("claude")
	sb, _ := b.Session("codex")
	ctx := context.Background()

	bobAtAlice := func() core.SignedPrekeyWire {
		p, _, err := a.Daemon.Peers().Resolve(ctx, "bob")
		if err != nil {
			t.Fatal(err)
		}
		return p.Prekey
	}
	old := bobAtAlice().ID

	Call(t, sb, ipc.MethodPeerPause, ipc.AliasParams{Alias: "alice"}, nil)
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

	id := sendChat(t, sa, "bob", "sealed to a deleted prekey")
	Eventually(t, wait, "message held while paused", func() bool { return a.Status().OutboxHeld >= 1 })
	Call(t, sb, ipc.MethodPeerResume, ipc.AliasParams{Alias: "alice"}, nil)

	WaitItem(t, sb, wait, "message after stale_prekey round trip", isChat(id))
	if got := bobAtAlice().ID; got == old {
		t.Fatal("alice still holds the purged prekey after delivery")
	}
	after := sendChat(t, sa, "bob", "sealed to the new prekey")
	_, seen := WaitItem(t, sb, wait, "follow-up message", isChat(after))
	for _, it := range seen {
		if it.ID == id {
			t.Fatal("the re-sealed message was shown twice")
		}
	}
	outbox := a.Daemon.Settings().(store.OutboxStore)
	Eventually(t, wait, "delivered receipt clears alice's outbox", func() bool {
		_, err := outbox.Get(ctx, id)
		return errors.Is(err, core.ErrNotFound)
	})
}

// The MCP server works against a real daemon: send_message and check_inbox.
func TestMCPSmoke(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t, PairOptions{})
	sb, _ := b.Session("codex")
	ctx := context.Background()

	srv, sess := mcpserver.New(mcpserver.Options{
		Dial:       func(ctx context.Context) (mcpserver.Conn, error) { return ipc.DialContext(ctx, a.Paths.Socket) },
		ProjectDir: a.Proj,
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

	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var buf bytes.Buffer
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				buf.WriteString(tc.Text)
			}
		}
		if res.IsError {
			t.Fatalf("%s failed: %s", name, buf.String())
		}
		return buf.String()
	}

	call("send_message", map[string]any{"to": "bob", "text": "hello over MCP"})
	got, _ := WaitItem(t, sb, wait, "MCP chat at bob", func(it ipc.InboxView) bool {
		return it.Kind == "chat" && strings.Contains(it.Wrapped, "hello over MCP")
	})
	if !strings.Contains(got.Wrapped, `session="claude@proj"`) {
		t.Fatalf("wrapped %q: want the MCP session name claude@proj (clientInfo claude-code, normalized)", got.Wrapped)
	}

	sendChat(t, sb, "alice", "reply for the agent")
	var text string
	Eventually(t, wait, "check_inbox shows the reply", func() bool {
		text += call("check_inbox", map[string]any{})
		return strings.Contains(text, "reply for the agent")
	})
	if !strings.Contains(text, "<remote_message") || !strings.Contains(text, `from="bob"`) {
		t.Fatalf("check_inbox output %q", text)
	}
	if again := call("check_inbox", map[string]any{}); again != mcpserver.NoMessagesText {
		t.Fatalf("second check_inbox = %q, want %q", again, mcpserver.NoMessagesText)
	}
}
