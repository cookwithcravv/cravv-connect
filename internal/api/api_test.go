package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/cravv/cravv-connect/internal/store"
)

const gpuID core.MachineID = "gpumachineid00000000"

type harness struct {
	w     *world
	clock *core.FakeClock
	sock  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &harness{w: newWorld(), clock: core.NewFakeClock(time.Unix(1_700_000_000, 0)), sock: filepath.Join(dir, "d.sock")}
	h.w.peers["gpu-box"] = store.Peer{MachineID: gpuID, Alias: "gpu-box", TrustIn: core.TrustAskFirst}
	srv := NewServer(h.w.ports(), h.clock, nil)
	ln, err := ipc.Listen(h.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return h
}

func (h *harness) dial(t *testing.T) *ipc.Client {
	t.Helper()
	c, err := ipc.Dial(h.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (h *harness) session(t *testing.T) *ipc.Client {
	t.Helper()
	c := h.dial(t)
	var r ipc.SessionRegisterResult
	if err := c.Call(context.Background(), ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: "/work/proj", PID: 42}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Name != "claude@proj" {
		t.Fatalf("name %q", r.Name)
	}
	return c
}

func unlock(t *testing.T, c *ipc.Client) {
	t.Helper()
	if err := c.Call(context.Background(), ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, nil); err != nil {
		t.Fatal(err)
	}
}

var bg = context.Background()

func TestEveryMethodRegisteredWithGate(t *testing.T) {
	w := newWorld()
	srv := NewServer(w.ports(), core.SystemClock{}, nil)
	want := map[string]ipc.Gate{
		ipc.MethodSessionRegister: ipc.GateNone,
		ipc.MethodStatus:          ipc.GateAllowWhenKilled,
		ipc.MethodChatSend:        ipc.GateSession,
		ipc.MethodInboxCheck:      ipc.GateSession,
		ipc.MethodInboxWait:       ipc.GateSession,
		ipc.MethodTaskCreate:      ipc.GateSession,
		ipc.MethodTaskGet:         ipc.GateSession,
		ipc.MethodTaskClaim:       ipc.GateSession,
		ipc.MethodTaskUpdate:      ipc.GateSession,
		ipc.MethodTaskComplete:    ipc.GateSession,
		ipc.MethodTaskFail:        ipc.GateSession,
		ipc.MethodTaskCancel:      ipc.GateSession,
		ipc.MethodFileSend:        ipc.GateSession,
		ipc.MethodPeerList:        ipc.GateAllowWhenKilled,
		ipc.MethodPeerPause:       ipc.GateNone,
		ipc.MethodPeerResume:      ipc.GateNone,
		ipc.MethodPeerUnpair:      ipc.GateNone,
		ipc.MethodPeerAlias:       ipc.GateNone,
		ipc.MethodPeerTrust:       ipc.GateNone,
		ipc.MethodKill:            ipc.GateNone,
		ipc.MethodResume:          ipc.GateUnlock | ipc.GateAllowWhenKilled,
		ipc.MethodAuthUnlock:      ipc.GateAllowWhenKilled,
		ipc.MethodPairStart:       ipc.GateUnlock,
		ipc.MethodPairAwait:       ipc.GateUnlock,
		ipc.MethodJoinStart:       ipc.GateUnlock,
		ipc.MethodPairFinalize:    ipc.GateUnlock,
		ipc.MethodApprovalsList:   ipc.GateUnlock,
		ipc.MethodApprovalsDecide: ipc.GateUnlock,
		ipc.MethodFilesList:       ipc.GateNone,
		ipc.MethodFilesAccept:     ipc.GateUnlock,
		ipc.MethodAllowPathAdd:    ipc.GateUnlock,
		ipc.MethodResetIdentity:   ipc.GateUnlock,
		ipc.MethodAuditRead:       ipc.GateAllowWhenKilled,
		ipc.MethodHookCounts:      ipc.GateAllowWhenKilled,
	}
	got := srv.Methods()
	if len(got) != len(want) {
		t.Errorf("registered %d methods, want %d", len(got), len(want))
	}
	for m, g := range want {
		if gg, ok := got[m]; !ok || gg != g {
			t.Errorf("%s: gate %b (registered %v), want %b", m, gg, ok, g)
		}
	}
}

func TestSessionRegisterValidation(t *testing.T) {
	h := newHarness(t)
	c := h.dial(t)
	if err := c.Call(bg, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "x", ProjectDir: "rel"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("relative dir: %v", err)
	}
	if err := c.Call(bg, ipc.MethodSessionRegister, ipc.SessionRegisterParams{ProjectDir: "/p"}, nil); err != nil {
		t.Fatal(err)
	}
	if h.w.lastCall() != "register agent /p" {
		t.Fatalf("empty agent not defaulted: %q", h.w.lastCall())
	}
	if err := c.Call(bg, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "y", ProjectDir: "/p"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("second register on one connection: %v", err)
	}
}

func TestChatSend(t *testing.T) {
	h := newHarness(t)
	c := h.session(t)
	var r ipc.IDResult
	if err := c.Call(bg, ipc.MethodChatSend, ipc.ChatSendParams{To: "gpu-box", Text: "hi"}, &r); err != nil || r.ID != "MSG1" {
		t.Fatalf("%v %+v", err, r)
	}
	if h.w.lastSession != "claude@proj" {
		t.Fatalf("session not passed: %q", h.w.lastSession)
	}
	cases := []struct {
		p    ipc.ChatSendParams
		want error
	}{
		{ipc.ChatSendParams{Text: "hi"}, ipc.ErrBadRequest},
		{ipc.ChatSendParams{To: "gpu-box"}, ipc.ErrBadRequest},
		{ipc.ChatSendParams{To: "gpu-box", Text: strings.Repeat("a", core.MaxTextBytes+1)}, core.ErrTooLarge},
	}
	for _, tc := range cases {
		if err := c.Call(bg, ipc.MethodChatSend, tc.p, nil); !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v, want %v", len(tc.p.Text), err, tc.want)
		}
	}
}

func TestInboxCheckPassesSessionAndLimit(t *testing.T) {
	h := newHarness(t)
	h.w.inbox = []ipc.InboxView{
		{Seq: 1, ID: "M1", From: "gpu-box", Kind: "chat", Wrapped: `<remote_message from="gpu-box">hi</remote_message>`},
		{Seq: 2, ID: "M2", From: "gpu-box", Kind: "task", TaskID: "T7", Wrapped: `<remote_message from="gpu-box">do</remote_message>`},
	}
	c := h.session(t)
	var r ipc.InboxResult
	if err := c.Call(bg, ipc.MethodInboxCheck, ipc.InboxCheckParams{}, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 || r.Items[1].TaskID != "T7" || r.Items[0].Wrapped != h.w.inbox[0].Wrapped {
		t.Fatalf("items %+v", r.Items)
	}
	if h.w.lastSession != "claude@proj" {
		t.Fatalf("session %q", h.w.lastSession)
	}
	if err := c.Call(bg, ipc.MethodInboxCheck, ipc.InboxCheckParams{Limit: 1}, &r); err != nil || len(r.Items) != 1 {
		t.Fatalf("limit: %v %d", err, len(r.Items))
	}
	if err := c.Call(bg, ipc.MethodInboxCheck, ipc.InboxCheckParams{Limit: 100000}, &r); err != nil || len(r.Items) != 2 {
		t.Fatalf("huge limit: %v %d", err, len(r.Items))
	}
}

func TestInboxWaitClampsTimeout(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want time.Duration
	}{{0, 50 * time.Second}, {-3, 50 * time.Second}, {5, 5 * time.Second}, {50, 50 * time.Second}, {600, 50 * time.Second}} {
		if got := WaitTimeout(tc.in); got != tc.want {
			t.Errorf("WaitTimeout(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
	h := newHarness(t)
	c := h.session(t)
	var r ipc.InboxResult
	if err := c.Call(bg, ipc.MethodInboxWait, ipc.InboxWaitParams{TimeoutS: 120}, &r); err != nil {
		t.Fatal(err)
	}
	if h.w.lastWait != 50*time.Second || r.Items == nil {
		t.Fatalf("wait %v items %v", h.w.lastWait, r.Items)
	}
}

func TestUnlockGatesPairing(t *testing.T) {
	h := newHarness(t)
	c := h.dial(t)
	if err := c.Call(bg, ipc.MethodPairStart, nil, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("pair without unlock: %v", err)
	}
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "wrong"}, nil); !errors.Is(err, core.ErrBadPassword) {
		t.Fatalf("bad password: %v", err)
	}
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{}, nil); !errors.Is(err, core.ErrBadPassword) {
		t.Fatalf("empty password: %v", err)
	}
	var u ipc.UnlockResult
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, &u); err != nil {
		t.Fatal(err)
	}
	if !u.ExpiresAt.Equal(h.clock.Now().Add(core.UnlockTTL)) {
		t.Fatalf("expires %v", u.ExpiresAt)
	}
	var ps ipc.PairStartResult
	if err := c.Call(bg, ipc.MethodPairStart, nil, &ps); err != nil || ps.Code == "" {
		t.Fatalf("pair.start: %v", err)
	}
	if err := c.Call(bg, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: "P1", Alias: "Bad Alias!", Trust: "ask-first"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("bad alias: %v", err)
	}
	if err := c.Call(bg, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: "P1", Alias: "gpu", Trust: "root"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("bad trust: %v", err)
	}
	var fin ipc.PairFinalizeResult
	if err := c.Call(bg, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: "P1", Alias: "gpu", Trust: "ask-first"}, &fin); err != nil || fin.Alias != "gpu" {
		t.Fatalf("finalize: %v %+v", err, fin)
	}
	h.clock.Advance(core.UnlockTTL)
	if err := c.Call(bg, ipc.MethodJoinStart, ipc.JoinStartParams{Code: "CRAVV-1"}, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("after expiry: %v", err)
	}
	other := h.dial(t)
	if err := other.Call(bg, ipc.MethodApprovalsList, nil, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("unlock leaked to another connection: %v", err)
	}
}

func TestTrustRaiseNeedsUnlockLowerDoesNot(t *testing.T) {
	h := newHarness(t)
	c := h.dial(t)
	if err := c.Call(bg, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "gpu-box", Level: "chat-only"}, nil); err != nil {
		t.Fatalf("lower: %v", err)
	}
	if h.w.trustSet != core.TrustChatOnly {
		t.Fatalf("trust %v", h.w.trustSet)
	}
	if err := c.Call(bg, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "gpu-box", Level: "autonomous"}, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("raise without unlock: %v", err)
	}
	if err := c.Call(bg, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "gpu-box", Level: "ask-first"}, nil); err != nil {
		t.Fatalf("same level: %v", err)
	}
	unlock(t, c)
	if err := c.Call(bg, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "gpu-box", Level: "autonomous"}, nil); err != nil {
		t.Fatalf("raise with unlock: %v", err)
	}
	if err := c.Call(bg, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "nobody", Level: "chat-only"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown peer: %v", err)
	}
	if err := c.Call(bg, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: "gpu-box", Level: "max"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("bad level: %v", err)
	}
}

func TestKillSwitchBlocksAllButAllowed(t *testing.T) {
	h := newHarness(t)
	c := h.session(t)
	if err := c.Call(bg, ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(bg, ipc.MethodChatSend, ipc.ChatSendParams{To: "gpu-box", Text: "x"}, nil); !errors.Is(err, core.ErrKilled) {
		t.Fatalf("chat while killed: %v", err)
	}
	if err := c.Call(bg, ipc.MethodPeerPause, ipc.AliasParams{Alias: "gpu-box"}, nil); !errors.Is(err, core.ErrKilled) {
		t.Fatalf("pause while killed: %v", err)
	}
	var st ipc.StatusResult
	if err := c.Call(bg, ipc.MethodStatus, nil, &st); err != nil || !st.Killed {
		t.Fatalf("status while killed: %v %+v", err, st)
	}
	var pl ipc.PeerListResult
	if err := c.Call(bg, ipc.MethodPeerList, nil, &pl); err != nil || len(pl.Peers) != 1 || !pl.Peers[0].Online {
		t.Fatalf("peer.list while killed: %v %+v", err, pl)
	}
	for _, m := range []string{ipc.MethodAuditRead, ipc.MethodHookCounts} {
		if err := c.Call(bg, m, nil, nil); err != nil {
			t.Fatalf("%s while killed: %v", m, err)
		}
	}
	if err := c.Call(bg, ipc.MethodResume, nil, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("resume without unlock: %v", err)
	}
	unlock(t, c)
	if err := c.Call(bg, ipc.MethodResume, nil, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := c.Call(bg, ipc.MethodChatSend, ipc.ChatSendParams{To: "gpu-box", Text: "x"}, nil); err != nil {
		t.Fatalf("chat after resume: %v", err)
	}
}

func TestDisconnectEndsSession(t *testing.T) {
	h := newHarness(t)
	c := h.session(t)
	c.Close()
	select {
	case name := <-h.w.disconnected:
		if name != "claude@proj" {
			t.Fatalf("name %q", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Sessions.Disconnect not called")
	}
}

func TestTaskMethods(t *testing.T) {
	h := newHarness(t)
	c := h.session(t)
	var cr ipc.TaskCreateResult
	if err := c.Call(bg, ipc.MethodTaskCreate, ipc.TaskCreateParams{To: "gpu-box", Instructions: "go"}, &cr); err != nil || cr.TaskID != "T1" {
		t.Fatalf("create: %v", err)
	}
	if h.w.lastSession != "claude@proj" || h.w.lastProject != "/work/proj" {
		t.Fatalf("create got %q %q", h.w.lastSession, h.w.lastProject)
	}
	if err := c.Call(bg, ipc.MethodTaskCreate, ipc.TaskCreateParams{To: "gpu-box", Instructions: strings.Repeat("x", core.MaxTextBytes+1)}, nil); !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	var tv ipc.TaskView
	if err := c.Call(bg, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: "T1"}, &tv); err != nil || tv.Peer != "gpu-box" || tv.State != "queued" {
		t.Fatalf("get: %v %+v", err, tv)
	}
	if err := c.Call(bg, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: "missing"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := c.Call(bg, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: "T1"}, nil); !errors.Is(err, core.ErrAlreadyClaimed) {
		t.Fatalf("claim: %v", err)
	}
	if err := c.Call(bg, ipc.MethodTaskUpdate, ipc.TaskUpdateParams{TaskID: "T1"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("update without note: %v", err)
	}
	if err := c.Call(bg, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: "T1", Result: "done!"}, &tv); err != nil || tv.Result != "done!" || tv.State != "done" {
		t.Fatalf("complete: %v %+v", err, tv)
	}
	if err := c.Call(bg, ipc.MethodTaskFail, ipc.TaskFailParams{TaskID: "T1", Reason: "no"}, &tv); err != nil || tv.State != "failed" {
		t.Fatalf("fail: %v", err)
	}
	if err := c.Call(bg, ipc.MethodTaskCancel, ipc.TaskIDParams{}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("cancel without id: %v", err)
	}
}

func TestApprovalsPreviewAndDecide(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("é", 600)
	h.w.approvals = []store.Task{{ID: "T9", Peer: gpuID, Instructions: long, CreatedAt: h.clock.Now()}}
	c := h.dial(t)
	unlock(t, c)
	var r ipc.ApprovalsListResult
	if err := c.Call(bg, ipc.MethodApprovalsList, nil, &r); err != nil || len(r.Tasks) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	a := r.Tasks[0]
	if []rune(a.Preview)[499] != 'é' || len([]rune(a.Preview)) != PreviewRunes || a.Full != long || a.Size != len(long) || len(a.SHA256) != 64 || a.Peer != "gpu-box" {
		t.Fatalf("approval view %+v", a)
	}
	if err := c.Call(bg, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: "T9", Approve: true}, nil); err != nil || h.w.lastCall() != "approve T9" {
		t.Fatalf("decide: %v %q", err, h.w.lastCall())
	}
}

func TestFilesAndControl(t *testing.T) {
	h := newHarness(t)
	c := h.session(t)
	var fr ipc.FileSendResult
	if err := c.Call(bg, ipc.MethodFileSend, ipc.FileSendParams{To: "gpu-box", Path: "a.txt"}, &fr); err != nil || fr.FileID != "F1" {
		t.Fatalf("file.send: %v", err)
	}
	if h.w.lastCall() != "file gpu-box /work/proj a.txt" {
		t.Fatalf("file.send args %q", h.w.lastCall())
	}
	h.w.files["F2"] = store.FileRecord{FileID: "F2", Direction: store.TaskInbound, Peer: gpuID, Name: "x.bin", State: store.FileHeld, Size: 9}
	var fl ipc.FilesListResult
	if err := c.Call(bg, ipc.MethodFilesList, nil, &fl); err != nil || len(fl.Files) != 1 || fl.Files[0].Peer != "gpu-box" || fl.Files[0].State != "held" {
		t.Fatalf("files.list: %v %+v", err, fl)
	}
	if err := c.Call(bg, ipc.MethodFilesAccept, ipc.FileIDParams{FileID: "F2"}, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("accept without unlock: %v", err)
	}
	unlock(t, c)
	if err := c.Call(bg, ipc.MethodFilesAccept, ipc.FileIDParams{FileID: "F2"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(bg, ipc.MethodAllowPathAdd, ipc.AllowPathParams{Path: "rel/dir"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("relative allow-path: %v", err)
	}
	if err := c.Call(bg, ipc.MethodAllowPathAdd, ipc.AllowPathParams{Path: "/data/x/../shared"}, nil); err != nil || h.w.lastCall() != "allow /data/shared" {
		t.Fatalf("allow-path: %v %q", err, h.w.lastCall())
	}
	if err := c.Call(bg, ipc.MethodPeerAlias, ipc.PeerAliasParams{Alias: "gpu-box", NewAlias: "GPU"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("bad new alias: %v", err)
	}
	if err := c.Call(bg, ipc.MethodPeerAlias, ipc.PeerAliasParams{Alias: "gpu-box", NewAlias: "gpu"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestHookCounts(t *testing.T) {
	h := newHarness(t)
	h.w.unread = map[string]int{"gpu-box": 2}
	h.w.pending = 1
	c := h.dial(t)
	var r ipc.HookCountsResult
	if err := c.Call(bg, ipc.MethodHookCounts, ipc.HookCountsParams{Cwd: "/work/proj"}, &r); err != nil {
		t.Fatal(err)
	}
	if r.Notice != present.Notice(map[string]int{"gpu-box": 2}, 1) || r.Notice == "" || r.Unread != 2 || r.Approvals != 1 {
		t.Fatalf("%+v", r)
	}
	h.w.unread, h.w.pending = nil, 0
	if err := c.Call(bg, ipc.MethodHookCounts, ipc.HookCountsParams{Cwd: "/work/proj"}, &r); err != nil || r.Notice != "" {
		t.Fatalf("empty: %v %+v", err, r)
	}
}
