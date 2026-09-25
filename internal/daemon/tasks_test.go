package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestTaskCreateSendsAndRecords(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id, err := e.tasks.Create(ctx, "claude@proj", "/w/proj", "gpu-box/codex@train", "run the tests", []string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	sent := e.sender.ofKind(core.KindTaskCreate)
	if len(sent) != 1 || sent[0].To != peer.MachineID || sent[0].FromSession != "claude@proj" || sent[0].ToSession != "codex@train" {
		t.Fatalf("sent = %+v", sent)
	}
	var body core.TaskCreateBody
	json.Unmarshal(sent[0].Body, &body)
	if body.TaskID != id || body.Instructions != "run the tests" || len(body.Files) != 2 {
		t.Fatalf("body = %+v", body)
	}
	if len(e.files.calls) != 2 || e.files.calls[0] != id+":a.txt" {
		t.Fatalf("file calls = %v", e.files.calls)
	}
	tk := e.state(t, id)
	if tk.Direction != store.TaskOutbound || tk.State != core.TaskSent || tk.FromSession != "claude@proj" || tk.ToSession != "codex@train" {
		t.Fatalf("mirror = %+v", tk)
	}
}

func TestTaskCreateRefusals(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	paused, _ := d2Peer(t, e.st, "old-mac", core.TrustAutonomous)
	paused.Paused = true
	e.st.PutPeer(ctx, paused)
	cases := []struct {
		name, to, text string
		want           error
	}{
		{"too large", "gpu-box", strings.Repeat("x", core.MaxTextBytes+1), core.ErrTooLarge},
		{"paused", "old-mac", "hi", core.ErrPaused},
		{"unknown peer", "nobody", "hi", core.ErrNotFound},
	}
	for _, c := range cases {
		if _, err := e.tasks.Create(ctx, "claude@proj", "/w", c.to, c.text, nil); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if len(e.sender.ofKind(core.KindTaskCreate)) != 0 {
		t.Fatal("refused task was sent")
	}

	// A peer that paused US is not a refusal: the task is queued (held by
	// Outbound) and delivered after control.resumed.
	pausedUs, _ := d2Peer(t, e.st, "away", core.TrustAutonomous)
	pausedUs.PausedByPeer = true
	e.st.PutPeer(ctx, pausedUs)
	if _, err := e.tasks.Create(ctx, "claude@proj", "/w", "away", "later is fine", nil); err != nil {
		t.Fatalf("create to a peer that paused us: %v", err)
	}
	if len(e.sender.ofKind(core.KindTaskCreate)) != 1 {
		t.Fatal("task to a peer that paused us was not enqueued")
	}
}

func TestInboundTaskByTrustLevel(t *testing.T) {
	cases := []struct {
		trust      core.TrustLevel
		state      core.TaskState
		update     core.TaskState
		updateNote string
		inInbox    bool
		desktop    bool
		expiresIn  time.Duration
	}{
		{core.TrustChatOnly, core.TaskRejected, core.TaskRejected, "not permitted", false, false, 0},
		{core.TrustAskFirst, core.TaskAwaitingApproval, core.TaskAwaitingApproval, "", false, true, core.ApprovalExpiry},
		{core.TrustAutonomous, core.TaskQueued, "", "", true, false, core.UnclaimedExpiry},
	}
	for _, c := range cases {
		t.Run(c.trust.String(), func(t *testing.T) {
			ctx := context.Background()
			e := d2Tasks(t)
			peer, _ := d2Peer(t, e.st, "gpu-box", c.trust)
			session, _ := e.reg.Register(ctx, "claude", "/w/proj")
			id := e.incoming(t, peer, "", "deploy it")

			tk := e.state(t, id)
			if tk.State != c.state || tk.Direction != store.TaskInbound || tk.FromSession != "codex@train" {
				t.Fatalf("task = %+v", tk)
			}
			if c.expiresIn == 0 && !tk.ExpiresAt.IsZero() || c.expiresIn != 0 && !tk.ExpiresAt.Equal(d2Epoch.Add(c.expiresIn)) {
				t.Fatalf("ExpiresAt = %v", tk.ExpiresAt)
			}
			ups := d2Updates(t, e.sender)
			if c.update == "" && len(ups) != 0 || c.update != "" && (len(ups) != 1 || ups[0].State != c.update || ups[0].Note != c.updateNote) {
				t.Fatalf("updates = %+v", ups)
			}
			if c.update != "" && e.sender.ofKind(core.KindTaskUpdate)[0].ToSession != "codex@train" {
				t.Fatal("update not addressed to the creating session")
			}
			items, _ := e.inbox.Check(ctx, session, 10)
			if c.inInbox != (len(items) == 1) {
				t.Fatalf("inbox items = %+v", items)
			}
			if c.inInbox && (items[0].Kind != "task" || items[0].Item.TaskID != id || !strings.Contains(items[0].Wrapped, "deploy it")) {
				t.Fatalf("inbox entry = %+v", items[0])
			}
			d := e.desktop.all()
			if c.desktop != (len(d) == 1) || c.desktop && d[0] != "cravv-connect: 1 task awaiting approval from gpu-box" {
				t.Fatalf("desktop = %v", d)
			}
			ev := e.audit.ofType(audit.EvTaskIn)
			if len(ev) != 1 || ev[0].ItemID != id || ev[0].Alias != "gpu-box" || ev[0].Hash != contentHash([]byte("deploy it")) {
				t.Fatalf("audit = %+v", ev)
			}
		})
	}
}

func TestInboundTaskDuplicateIgnored(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	session, _ := e.reg.Register(ctx, "claude", "/w/proj")
	env := d2Env(t, peer, core.KindTaskCreate, "", "", core.TaskCreateBody{TaskID: "T1", Instructions: "x"})
	for i := 0; i < 2; i++ {
		if err := e.tasks.HandleCreate(ctx, peer, env); err != nil {
			t.Fatal(err)
		}
	}
	if items, _ := e.inbox.Check(ctx, session, 10); len(items) != 1 {
		t.Fatalf("duplicate task.create shown %d times", len(items))
	}
}

func TestInboundTaskTargetsSession(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	a, _ := e.reg.Register(ctx, "claude", "/w/proj")
	b, _ := e.reg.Register(ctx, "claude", "/w/proj")
	e.incoming(t, peer, b, "only for b")
	if items, _ := e.inbox.Check(ctx, a, 10); len(items) != 0 {
		t.Fatalf("session a saw a task for b")
	}
	if items, _ := e.inbox.Check(ctx, b, 10); len(items) != 1 {
		t.Fatalf("session b did not see its task")
	}
}

func TestClaimRace(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id := e.incoming(t, peer, "", "race me")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []string{"claude@proj", "claude@proj-2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.tasks.Claim(ctx, s, id)
		}()
	}
	wg.Wait()
	wins, lost := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, core.ErrAlreadyClaimed):
			lost++
		default:
			t.Fatalf("unexpected claim error %v", err)
		}
	}
	if wins != 1 || lost != 1 {
		t.Fatalf("wins=%d lost=%d", wins, lost)
	}
	tk := e.state(t, id)
	if tk.State != core.TaskClaimed || !tk.ExpiresAt.IsZero() {
		t.Fatalf("task = %+v", tk)
	}
	if again, err := e.tasks.Claim(ctx, tk.ClaimedBy, id); err != nil || again.ClaimedBy != tk.ClaimedBy {
		t.Fatalf("re-claim by owner: %v", err)
	}
}

func TestOnlyClaimerMayProgress(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id := e.incoming(t, peer, "", "work")
	if _, err := e.tasks.Update(ctx, "claude@proj", id, "early"); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("update before claim err = %v", err)
	}
	if _, err := e.tasks.Claim(ctx, "claude@proj", id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tasks.Update(ctx, "other@x", id, "hijack"); !errors.Is(err, core.ErrNotPermitted) {
		t.Fatalf("non-claimer update err = %v", err)
	}
	if _, err := e.tasks.Complete(ctx, "other@x", "/w", id, "done", nil); !errors.Is(err, core.ErrNotPermitted) {
		t.Fatalf("non-claimer complete err = %v", err)
	}
	if _, err := e.tasks.Fail(ctx, "other@x", id, "nope"); !errors.Is(err, core.ErrNotPermitted) {
		t.Fatalf("non-claimer fail err = %v", err)
	}
	tk, err := e.tasks.Update(ctx, "claude@proj", id, "halfway")
	if err != nil || tk.State != core.TaskRunning || len(tk.Notes) != 1 || tk.Notes[0].Text != "halfway" {
		t.Fatalf("update: %+v %v", tk, err)
	}
	if _, err := e.tasks.Complete(ctx, "claude@proj", "/w", id, strings.Repeat("r", core.MaxTextBytes+1), nil); !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("oversize result err = %v", err)
	}
	tk, err = e.tasks.Complete(ctx, "claude@proj", "/w", id, "all green", []string{"report.txt"})
	if err != nil || tk.State != core.TaskDone || tk.Result != "all green" || len(tk.ResultFiles) != 1 {
		t.Fatalf("complete: %+v %v", tk, err)
	}
	ups := d2Updates(t, e.sender)
	var states []core.TaskState
	for _, u := range ups {
		states = append(states, u.State)
	}
	want := []core.TaskState{core.TaskClaimed, core.TaskRunning, core.TaskDone}
	if len(states) != 3 || states[0] != want[0] || states[1] != want[1] || states[2] != want[2] {
		t.Fatalf("update states = %v", states)
	}
	if ups[2].Result != "all green" || len(ups[2].Files) != 1 {
		t.Fatalf("done update = %+v", ups[2])
	}
	if _, err := e.tasks.Fail(ctx, "claude@proj", id, "late"); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("fail after done err = %v", err)
	}
}

func TestFailSendsReason(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id := e.incoming(t, peer, "", "work")
	e.tasks.Claim(ctx, "claude@proj", id)
	tk, err := e.tasks.Fail(ctx, "claude@proj", id, "missing dataset")
	if err != nil || tk.State != core.TaskFailed {
		t.Fatalf("fail: %+v %v", tk, err)
	}
	ups := d2Updates(t, e.sender)
	if last := ups[len(ups)-1]; last.State != core.TaskFailed || last.Note != "missing dataset" {
		t.Fatalf("last update = %+v", last)
	}
}

func TestApprovalFlow(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAskFirst)
	session, _ := e.reg.Register(ctx, "claude", "/w/proj")
	long := strings.Repeat("é", 600)
	approveID := e.incoming(t, peer, "", long)
	e.clock.Advance(time.Second)
	denyID := e.incoming(t, peer, "", "rm -rf /")

	list, err := e.tasks.Approvals(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("approvals = %+v %v", list, err)
	}
	a := list[0]
	if a.Task.ID != approveID || a.Alias != "gpu-box" || len([]rune(a.Preview)) != ApprovalPreviewChars ||
		a.SHA256 != contentHash([]byte(long)) || a.Size != len(long) {
		t.Fatalf("approval = %+v", a)
	}
	if n, _ := e.tasks.PendingApprovals(ctx); n != 2 {
		t.Fatalf("pending = %d", n)
	}
	if _, err := e.tasks.Claim(ctx, session, approveID); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("claim before approval err = %v", err)
	}

	if err := e.tasks.Decide(ctx, approveID, true, true); err != nil {
		t.Fatal(err)
	}
	if err := e.tasks.Decide(ctx, denyID, false, true); err != nil {
		t.Fatal(err)
	}
	if tk := e.state(t, approveID); tk.State != core.TaskQueued || !tk.ExpiresAt.Equal(e.clock.Now().Add(core.UnclaimedExpiry)) {
		t.Fatalf("approved = %+v", tk)
	}
	if tk := e.state(t, denyID); tk.State != core.TaskRejected {
		t.Fatalf("denied = %+v", tk)
	}
	items, _ := e.inbox.Check(ctx, session, 10)
	if len(items) != 1 || items[0].Item.TaskID != approveID {
		t.Fatalf("inbox after decisions = %+v", items)
	}
	if len(e.audit.ofType(audit.EvApprove)) != 1 || len(e.audit.ofType(audit.EvDeny)) != 1 {
		t.Fatal("approve/deny not audited")
	}
	ups := d2Updates(t, e.sender)
	last := ups[len(ups)-1]
	if last.TaskID != denyID || last.State != core.TaskRejected {
		t.Fatalf("deny update = %+v", last)
	}
	if err := e.tasks.Decide(ctx, approveID, true, true); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("second decision err = %v", err)
	}
	if _, err := e.tasks.Claim(ctx, session, approveID); err != nil {
		t.Fatalf("claim after approval: %v", err)
	}
}

func TestExpireDue(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	auto, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	ask, _ := d2Peer(t, e.st, "mac", core.TrustAskFirst)
	queued := e.incoming(t, auto, "", "a")
	held := e.incoming(t, ask, "", "b")
	claimed := e.incoming(t, auto, "", "c")
	e.tasks.Claim(ctx, "claude@proj", claimed)

	e.clock.Advance(23 * time.Hour)
	if n, err := e.tasks.ExpireDue(ctx); err != nil || n != 0 {
		t.Fatalf("expired early: %d %v", n, err)
	}
	e.clock.Advance(2 * time.Hour)
	if n, err := e.tasks.ExpireDue(ctx); err != nil || n != 2 {
		t.Fatalf("ExpireDue = %d %v, want 2", n, err)
	}
	for _, id := range []string{queued, held} {
		if st := e.state(t, id).State; st != core.TaskExpired {
			t.Fatalf("%s state = %s", id, st)
		}
	}
	if st := e.state(t, claimed).State; st != core.TaskClaimed {
		t.Fatalf("claimed task expired: %s", st)
	}
	expiredUpdates := 0
	for _, u := range d2Updates(t, e.sender) {
		if u.State == core.TaskExpired {
			expiredUpdates++
		}
	}
	if expiredUpdates != 2 {
		t.Fatalf("expired updates = %d", expiredUpdates)
	}
	if _, err := e.tasks.Claim(ctx, "claude@proj", queued); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("claim expired err = %v", err)
	}
}

func TestAbandonSession(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	mine := e.incoming(t, peer, "", "a")
	theirs := e.incoming(t, peer, "", "b")
	e.tasks.Claim(ctx, "claude@proj", mine)
	e.tasks.Update(ctx, "claude@proj", mine, "started")
	e.tasks.Claim(ctx, "codex@proj", theirs)
	if err := e.tasks.AbandonSession(ctx, "claude@proj"); err != nil {
		t.Fatal(err)
	}
	tk := e.state(t, mine)
	if tk.State != core.TaskFailed || tk.Notes[len(tk.Notes)-1].Text != "abandoned" {
		t.Fatalf("abandoned task = %+v", tk)
	}
	if e.state(t, theirs).State != core.TaskClaimed {
		t.Fatal("other session's task touched")
	}
	ups := d2Updates(t, e.sender)
	if last := ups[len(ups)-1]; last.TaskID != mine || last.State != core.TaskFailed || last.Note != "abandoned" {
		t.Fatalf("last update = %+v", last)
	}
}

func TestFailActiveOnKill(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	a := e.incoming(t, peer, "", "a")
	b := e.incoming(t, peer, "", "b")
	e.tasks.Claim(ctx, "claude@proj", a)
	if err := e.tasks.FailActive(ctx, "killed"); err != nil {
		t.Fatal(err)
	}
	if e.state(t, a).State != core.TaskFailed || e.state(t, b).State != core.TaskQueued {
		t.Fatal("FailActive touched the wrong tasks")
	}
}

func TestCancelSenderSide(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	id, _ := e.tasks.Create(ctx, "claude@proj", "/w", "gpu-box", "long job", nil)
	if _, err := e.tasks.Cancel(ctx, "codex@proj", id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cancel by another session err = %v", err)
	}
	tk, err := e.tasks.Cancel(ctx, "claude@proj", id)
	if err != nil || tk.State != core.TaskCancelled {
		t.Fatalf("cancel: %+v %v", tk, err)
	}
	c := e.sender.ofKind(core.KindTaskCancel)
	if len(c) != 1 || c[0].To != peer.MachineID {
		t.Fatalf("cancel sent = %+v", c)
	}
	if _, err := e.tasks.Cancel(ctx, "claude@proj", id); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("second cancel err = %v", err)
	}
	inbound := e.incoming(t, peer, "", "x")
	if _, err := e.tasks.Cancel(ctx, "claude@proj", inbound); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cancel of inbound task err = %v", err)
	}
}

func TestCancelReceiverSide(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	stranger, _ := d2Peer(t, e.st, "mac", core.TrustAutonomous)
	claimer, _ := e.reg.Register(ctx, "claude", "/w/proj")
	id := e.incoming(t, peer, "", "job")
	e.tasks.Claim(ctx, claimer, id)
	e.inbox.Check(ctx, claimer, 10) // read the task itself

	bad := d2Env(t, stranger, core.KindTaskCancel, "", "", core.TaskCancelBody{TaskID: id})
	if err := e.tasks.HandleCancel(ctx, stranger, bad); err != nil {
		t.Fatal(err)
	}
	if e.state(t, id).State != core.TaskClaimed {
		t.Fatal("another peer cancelled the task")
	}
	good := d2Env(t, peer, core.KindTaskCancel, "", "", core.TaskCancelBody{TaskID: id})
	if err := e.tasks.HandleCancel(ctx, peer, good); err != nil {
		t.Fatal(err)
	}
	if e.state(t, id).State != core.TaskCancelled {
		t.Fatal("task not cancelled")
	}
	items, _ := e.inbox.Check(ctx, claimer, 10)
	if len(items) != 1 || items[0].Kind != "task_update" || !strings.Contains(items[0].Wrapped, "cancelled by sender") {
		t.Fatalf("claimer notice = %+v", items)
	}
	if _, err := e.tasks.Complete(ctx, claimer, "/w", id, "done anyway", nil); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("complete after cancel err = %v", err)
	}
}

func TestHandleUpdateMirror(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	stranger, _ := d2Peer(t, e.st, "mac", core.TrustAutonomous)
	creator, _ := e.reg.Register(ctx, "claude", "/w/proj")
	other, _ := e.reg.Register(ctx, "claude", "/w/proj")
	id, err := e.tasks.Create(ctx, creator, "/w/proj", "gpu-box", "train", nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(from store.Peer, body core.TaskUpdateBody) {
		env := d2Env(t, from, core.KindTaskUpdate, "codex@train", creator, body)
		if err := e.tasks.HandleUpdate(ctx, from, env); err != nil {
			t.Fatal(err)
		}
	}
	send(stranger, core.TaskUpdateBody{TaskID: id, State: core.TaskDone, Result: "forged"})
	if e.state(t, id).State != core.TaskSent {
		t.Fatal("stranger updated our task")
	}
	send(peer, core.TaskUpdateBody{TaskID: id, State: core.TaskRunning, Note: "epoch 1"})
	send(peer, core.TaskUpdateBody{TaskID: id, State: core.TaskDone, Result: "loss 0.1"})
	send(peer, core.TaskUpdateBody{TaskID: id, State: core.TaskClaimed})
	tk := e.state(t, id)
	if tk.State != core.TaskDone || tk.Result != "loss 0.1" || len(tk.Notes) != 1 || tk.ClaimedBy != "codex@train" {
		t.Fatalf("mirror = %+v", tk)
	}
	items, _ := e.inbox.Check(ctx, creator, 10)
	if len(items) != 2 || items[0].Kind != "task_update" || !strings.Contains(items[1].Wrapped, "loss 0.1") {
		t.Fatalf("creator inbox = %+v", items)
	}
	if got, _ := e.inbox.Check(ctx, other, 10); len(got) != 0 {
		t.Fatalf("other session saw %d task updates", len(got))
	}
	if got, err := e.tasks.Get(ctx, creator, id); err != nil || got.ID != id {
		t.Fatalf("creator Get: %v", err)
	}
	if _, err := e.tasks.Get(ctx, other, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other session Get err = %v", err)
	}
}

func TestRecheckPeerRejectsPending(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "gpu-box", core.TrustAutonomous)
	queued := e.incoming(t, peer, "", "a")
	claimed := e.incoming(t, peer, "", "b")
	e.tasks.Claim(ctx, "claude@proj", claimed)
	if err := e.tasks.RecheckPeer(ctx, peer.MachineID, core.TrustAskFirst); err != nil {
		t.Fatal(err)
	}
	if e.state(t, queued).State != core.TaskQueued {
		t.Fatal("ask-first recheck changed a queued task")
	}
	peer.TrustIn = core.TrustChatOnly
	var obs TrustObserver = e.tasks
	if err := obs.TrustLowered(ctx, peer); err != nil {
		t.Fatal(err)
	}
	if e.state(t, queued).State != core.TaskRejected || e.state(t, claimed).State != core.TaskClaimed {
		t.Fatal("chat-only recheck wrong")
	}
}
