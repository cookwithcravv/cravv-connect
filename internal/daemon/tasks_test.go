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

func TestTaskCreateSendsOnTheLink(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermMessages)
	id, err := e.tasks.Create(ctx, e.session.ID, "/w/proj", e.link.Num, "run the tests", []string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	sent := e.sender.ofKind(core.KindTaskCreate)
	if len(sent) != 1 || sent[0].To != e.peer.MachineID || sent[0].LinkID != e.link.ID {
		t.Fatalf("sent = %+v", sent)
	}
	var body core.TaskCreateBody
	json.Unmarshal(sent[0].Body, &body)
	if body.TaskID != id || body.Instructions != "run the tests" || len(body.Files) != 2 {
		t.Fatalf("body = %+v", body)
	}
	if len(e.files.calls) != 2 || e.files.calls[0] != id+":a.txt" || e.files.links[0] != e.link.ID {
		t.Fatalf("file calls = %v on %v", e.files.calls, e.files.links)
	}
	tk := e.state(t, id)
	if tk.Direction != store.TaskOutbound || tk.State != core.TaskSent || tk.FromSession != e.session.ID ||
		tk.ToSession != "trainer" || tk.LinkID != e.link.ID {
		t.Fatalf("mirror = %+v", tk)
	}
}

func TestTaskCreateRefusals(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermMessages)
	other := d2Share(t, e.shared, "other")
	msgOnly := d2Link(t, e.st, e.peer, e.session, "reader", core.PermMessages, core.PermMessages)
	cases := []struct {
		name    string
		session string
		link    int64
		text    string
		want    error
	}{
		{"too large", e.session.ID, e.link.Num, strings.Repeat("x", core.MaxTextBytes+1), core.ErrTooLarge},
		{"another session's link", other.ID, e.link.Num, "hi", core.ErrNotFound},
		{"no such link", e.session.ID, 999, "hi", core.ErrNotFound},
		{"the peer allows messages only", e.session.ID, msgOnly.Num, "hi", core.ErrNotPermitted},
	}
	for _, c := range cases {
		if _, err := e.tasks.Create(ctx, c.session, "/w", c.link, c.text, nil); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if err := e.links.Disconnect(ctx, e.session.ID, e.link.Num); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tasks.Create(ctx, e.session.ID, "/w", e.link.Num, "hi", nil); !errors.Is(err, core.ErrLinkClosed) {
		t.Fatalf("closed link: %v", err)
	}
	if len(e.sender.ofKind(core.KindTaskCreate)) != 0 {
		t.Fatal("refused task was sent")
	}
}

func TestInboundTaskByPermission(t *testing.T) {
	cases := []struct {
		perm       core.Permission
		state      core.TaskState
		update     core.TaskState
		updateNote string
		inInbox    bool
		desktop    bool
		expiresIn  time.Duration
	}{
		{core.PermMessages, core.TaskRejected, core.TaskRejected, "not permitted", false, false, 0},
		{core.PermTasksAsk, core.TaskAwaitingApproval, core.TaskAwaitingApproval, "", false, true, core.ApprovalExpiry},
		{core.PermTasksAuto, core.TaskQueued, "", "", true, false, core.UnclaimedExpiry},
	}
	for _, c := range cases {
		t.Run(string(c.perm), func(t *testing.T) {
			ctx := context.Background()
			e := d2Tasks(t, c.perm)
			id := e.incoming(t, "deploy it")

			tk := e.state(t, id)
			if tk.State != c.state || tk.Direction != store.TaskInbound || tk.FromSession != "trainer" ||
				tk.ToSession != e.session.ID || tk.LinkID != e.link.ID {
				t.Fatalf("task = %+v", tk)
			}
			if c.expiresIn == 0 && !tk.ExpiresAt.IsZero() || c.expiresIn != 0 && !tk.ExpiresAt.Equal(d2Epoch.Add(c.expiresIn)) {
				t.Fatalf("ExpiresAt = %v", tk.ExpiresAt)
			}
			ups := d2Updates(t, e.sender)
			if c.update == "" && len(ups) != 0 || c.update != "" && (len(ups) != 1 || ups[0].State != c.update || ups[0].Note != c.updateNote) {
				t.Fatalf("updates = %+v", ups)
			}
			if c.update != "" && e.sender.ofKind(core.KindTaskUpdate)[0].LinkID != e.link.ID {
				t.Fatal("update not sent on the task's link")
			}
			items, _ := e.inbox.Check(ctx, e.session.ID, 10)
			if c.inInbox != (len(items) == 1) {
				t.Fatalf("inbox items = %+v", items)
			}
			if c.inInbox && (items[0].Kind != "task" || items[0].Item.TaskID != id || !strings.Contains(items[0].Wrapped, "deploy it") ||
				!strings.Contains(items[0].Wrapped, `permission="tasks-auto"`)) {
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
	e := d2Tasks(t, core.PermTasksAuto)
	env := d2Env(t, e.peer, core.KindTaskCreate, e.link.ID, core.TaskCreateBody{TaskID: core.NewID(), Instructions: "x"})
	for range 2 {
		if err := e.handle(t, env); err != nil {
			t.Fatal(err)
		}
	}
	if items, _ := e.inbox.Check(ctx, e.session.ID, 10); len(items) != 1 {
		t.Fatalf("duplicate task.create shown %d times", len(items))
	}
}

// Traffic on one link is never visible to another session (v2 success
// criterion 2): a task reaches only the link's own session.
func TestInboundTaskReachesOnlyTheLinksSession(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	other := d2Share(t, e.shared, "other")
	id := e.incoming(t, "only for lead")
	if items, _ := e.inbox.Check(ctx, other.ID, 10); len(items) != 0 {
		t.Fatal("another session saw the task")
	}
	for _, op := range []func() error{
		func() error { _, err := e.tasks.Get(ctx, other.ID, id); return err },
		func() error { _, err := e.tasks.Claim(ctx, other.ID, id); return err },
	} {
		if err := op(); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("another session reached the task: %v", err)
		}
	}
	if items, _ := e.inbox.Check(ctx, e.session.ID, 10); len(items) != 1 {
		t.Fatal("the link's session did not see its task")
	}
}

func TestClaimIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	id := e.incoming(t, "race me")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.tasks.Claim(ctx, e.session.ID, id)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("claim by the owning session: %v", err)
		}
	}
	tk := e.state(t, id)
	if tk.State != core.TaskClaimed || !tk.ExpiresAt.IsZero() || tk.ClaimedBy != e.session.ID {
		t.Fatalf("task = %+v", tk)
	}
	if n := len(d2Updates(t, e.sender)); n != 1 {
		t.Fatalf("%d claimed updates sent, want 1", n)
	}
}

func TestOnlyClaimerMayProgress(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	id := e.incoming(t, "work")
	s := e.session.ID
	if _, err := e.tasks.Update(ctx, s, id, "early"); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("update before claim err = %v", err)
	}
	if _, err := e.tasks.Claim(ctx, s, id); err != nil {
		t.Fatal(err)
	}
	other := d2Share(t, e.shared, "other").ID
	if _, err := e.tasks.Update(ctx, other, id, "hijack"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session's update err = %v", err)
	}
	if _, err := e.tasks.Complete(ctx, other, "/w", id, "done", nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session's complete err = %v", err)
	}
	if _, err := e.tasks.Fail(ctx, other, id, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session's fail err = %v", err)
	}
	tk, err := e.tasks.Update(ctx, s, id, "halfway")
	if err != nil || tk.State != core.TaskRunning || len(tk.Notes) != 1 || tk.Notes[0].Text != "halfway" {
		t.Fatalf("update: %+v %v", tk, err)
	}
	if _, err := e.tasks.Complete(ctx, s, "/w", id, strings.Repeat("r", core.MaxTextBytes+1), nil); !errors.Is(err, core.ErrTooLarge) {
		t.Fatalf("oversize result err = %v", err)
	}
	tk, err = e.tasks.Complete(ctx, s, "/w", id, "all green", []string{"report.txt"})
	if err != nil || tk.State != core.TaskDone || tk.Result != "all green" || len(tk.ResultFiles) != 1 {
		t.Fatalf("complete: %+v %v", tk, err)
	}
	if e.files.links[0] != e.link.ID {
		t.Fatal("result file not sent on the task's link")
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
	if _, err := e.tasks.Fail(ctx, s, id, "late"); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("fail after done err = %v", err)
	}
}

func TestFailSendsReason(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	id := e.incoming(t, "work")
	e.tasks.Claim(ctx, e.session.ID, id)
	tk, err := e.tasks.Fail(ctx, e.session.ID, id, "missing dataset")
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
	e := d2Tasks(t, core.PermTasksAsk)
	long := strings.Repeat("é", 600)
	approveID := e.incoming(t, long)
	e.clock.Advance(time.Second)
	denyID := e.incoming(t, "rm -rf /")

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
	if _, err := e.tasks.Claim(ctx, e.session.ID, approveID); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("claim before approval err = %v", err)
	}
	if err := e.tasks.Decide(ctx, approveID, true, AuthNone); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("approval without a human decision: %v", err)
	}
	if err := e.tasks.Decide(ctx, approveID, true, AuthChat); err != nil {
		t.Fatalf("approval from a chat decision: %v", err)
	}
	if err := e.tasks.Decide(ctx, denyID, false, AuthNone); err != nil {
		t.Fatalf("denying needs no authority: %v", err)
	}
	if tk := e.state(t, approveID); tk.State != core.TaskQueued || !tk.ExpiresAt.Equal(e.clock.Now().Add(core.UnclaimedExpiry)) {
		t.Fatalf("approved = %+v", tk)
	}
	if tk := e.state(t, denyID); tk.State != core.TaskRejected {
		t.Fatalf("denied = %+v", tk)
	}
	items, _ := e.inbox.Check(ctx, e.session.ID, 10)
	if len(items) != 1 || items[0].Item.TaskID != approveID {
		t.Fatalf("inbox after decisions = %+v", items)
	}
	if len(e.audit.ofType(audit.EvApprove)) != 1 || len(e.audit.ofType(audit.EvDeny)) != 1 {
		t.Fatal("approve/deny not audited")
	}
	var told []string
	for _, u := range d2Updates(t, e.sender) {
		if u.TaskID == approveID && u.State == core.TaskQueued || u.TaskID == denyID && u.State == core.TaskRejected {
			told = append(told, string(u.State))
		}
	}
	if len(told) != 2 {
		t.Fatalf("sender told %v, want queued and rejected", told)
	}
	if err := e.tasks.Decide(ctx, approveID, true, AuthPassword); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("second decision err = %v", err)
	}
	if _, err := e.tasks.Claim(ctx, e.session.ID, approveID); err != nil {
		t.Fatalf("claim after approval: %v", err)
	}
}

func TestApprovalRefusedAfterTheLinkChanged(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk)
	held := e.incoming(t, "held")
	stillHeld := e.incoming(t, "also held")
	// Lowering to messages rejects the waiting tasks.
	if _, err := e.links.SetPermission(ctx, "", e.link.Num, core.PermMessages, AuthNone); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{held, stillHeld} {
		if st := e.state(t, id).State; st != core.TaskRejected {
			t.Fatalf("waiting task after lowering to messages: %s", st)
		}
	}
	// A task held on a link that then closed cannot be approved.
	l2 := d2Link(t, e.st, e.peer, e.session, "second", core.PermTasksAsk, core.PermMessages)
	id := core.NewID()
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCreate, l2.ID, core.TaskCreateBody{TaskID: id, Instructions: "x"})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UpdateLink(ctx, e.peer.MachineID, l2.ID, func(l *store.Link) error {
		l.State = store.LinkClosed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.tasks.Decide(ctx, id, true, AuthPassword); !errors.Is(err, core.ErrLinkClosed) {
		t.Fatalf("approval on a closed link: %v", err)
	}
}

func TestExpireDue(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	askLink := d2Link(t, e.st, e.peer, e.session, "asker", core.PermTasksAsk, core.PermMessages)
	queued := e.incoming(t, "a")
	held := core.NewID()
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCreate, askLink.ID, core.TaskCreateBody{TaskID: held, Instructions: "b"})); err != nil {
		t.Fatal(err)
	}
	claimed := e.incoming(t, "c")
	e.tasks.Claim(ctx, e.session.ID, claimed)

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
	if _, err := e.tasks.Claim(ctx, e.session.ID, queued); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("claim expired err = %v", err)
	}
}

// Closing a link fails every unfinished task on it, in both directions, and
// leaves other links alone. The receiver tells the sender; the sender does
// not wait for that update (it may never come over a closed link).
func TestLinkCloseFailsItsTasks(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	other := d2Link(t, e.st, e.peer, e.session, "other", core.PermTasksAuto, core.PermTasksAuto)
	queued := e.incoming(t, "queued")
	running := e.incoming(t, "running")
	e.tasks.Claim(ctx, e.session.ID, running)
	e.tasks.Update(ctx, e.session.ID, running, "working")
	outbound, err := e.tasks.Create(ctx, e.session.ID, "/w", e.link.Num, "mine", nil)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := core.NewID()
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCreate, other.ID, core.TaskCreateBody{TaskID: elsewhere, Instructions: "x"})); err != nil {
		t.Fatal(err)
	}
	before := len(d2Updates(t, e.sender))
	if err := e.links.Disconnect(ctx, e.session.ID, e.link.Num); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{queued, running, outbound} {
		tk := e.state(t, id)
		if tk.State != core.TaskFailed || tk.Notes[len(tk.Notes)-1].Text != ReasonLinkClosed {
			t.Fatalf("task %s after the link closed: %+v", id, tk)
		}
	}
	if st := e.state(t, elsewhere).State; st != core.TaskQueued {
		t.Fatalf("a task on another link changed to %s", st)
	}
	var told int
	for _, u := range d2Updates(t, e.sender)[before:] {
		if u.State == core.TaskFailed && u.Note == ReasonLinkClosed {
			told++
		}
	}
	if told != 2 {
		t.Fatalf("%d failed(link_closed) updates, want one per inbound task", told)
	}
	items, _ := e.inbox.Check(ctx, e.session.ID, 50)
	found := false
	for _, it := range items {
		if it.Item.TaskID == outbound && it.Kind == "task_update" && strings.Contains(it.Wrapped, "link_closed") {
			found = true
		}
	}
	if !found {
		t.Fatal("the session that sent the task was not told it failed")
	}
}

// Review focus: the receiver sends task.update{failed, link_closed} and then
// link.closed, but outbox order within one millisecond is not guaranteed and
// the update is dropped once the link is closed here. The sender must fail
// its tasks on the link from link.closed alone.
func TestPeerCloseFailsOutboundTasksWithoutTheirUpdate(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermMessages)
	id, err := e.tasks.Create(ctx, e.session.ID, "/w", e.link.Num, "long job", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskUpdate, e.link.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskRunning})); err != nil {
		t.Fatal(err)
	}
	closed := d2Env(t, e.peer, core.KindLinkClosed, "", core.LinkClosedBody{LinkID: e.link.ID, Reason: core.CloseSessionClosed})
	if err := e.links.HandleClosed(ctx, e.peer, closed); err != nil {
		t.Fatal(err)
	}
	tk := e.state(t, id)
	if tk.State != core.TaskFailed || tk.Notes[len(tk.Notes)-1].Text != ReasonLinkClosed {
		t.Fatalf("outbound task after the peer closed the link: %+v", tk)
	}
	// The update that lost the race now arrives on a closed link: dropped.
	late := d2Env(t, e.peer, core.KindTaskUpdate, e.link.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskDone, Result: "too late"})
	if err := e.handle(t, late); err != nil {
		t.Fatal(err)
	}
	if tk := e.state(t, id); tk.State != core.TaskFailed || tk.Result != "" {
		t.Fatalf("a late update changed the task: %+v", tk)
	}
	if len(e.replies.unknown) != 1 {
		t.Fatalf("unknown_link replies %v, want one for the late update", e.replies.unknown)
	}
}

func TestFailActiveOnKill(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	a := e.incoming(t, "a")
	b := e.incoming(t, "b")
	e.tasks.Claim(ctx, e.session.ID, a)
	if err := e.tasks.FailActive(ctx, "killed"); err != nil {
		t.Fatal(err)
	}
	if e.state(t, a).State != core.TaskFailed || e.state(t, b).State != core.TaskQueued {
		t.Fatal("FailActive touched the wrong tasks")
	}
}

func TestCancelSenderSide(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	id, _ := e.tasks.Create(ctx, e.session.ID, "/w", e.link.Num, "long job", nil)
	other := d2Share(t, e.shared, "other")
	if _, err := e.tasks.Cancel(ctx, other.ID, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cancel by another session err = %v", err)
	}
	tk, err := e.tasks.Cancel(ctx, e.session.ID, id)
	if err != nil || tk.State != core.TaskCancelled {
		t.Fatalf("cancel: %+v %v", tk, err)
	}
	c := e.sender.ofKind(core.KindTaskCancel)
	if len(c) != 1 || c[0].To != e.peer.MachineID || c[0].LinkID != e.link.ID {
		t.Fatalf("cancel sent = %+v", c)
	}
	if _, err := e.tasks.Cancel(ctx, e.session.ID, id); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("second cancel err = %v", err)
	}
	inbound := e.incoming(t, "x")
	if _, err := e.tasks.Cancel(ctx, e.session.ID, inbound); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("cancel of inbound task err = %v", err)
	}
}

func TestCancelReceiverSide(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	id := e.incoming(t, "job")
	e.tasks.Claim(ctx, e.session.ID, id)
	e.inbox.Check(ctx, e.session.ID, 10) // read the task itself

	// The same peer on another link may not cancel it.
	other := d2Link(t, e.st, e.peer, e.session, "other", core.PermTasksAuto, core.PermTasksAuto)
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCancel, other.ID, core.TaskCancelBody{TaskID: id})); err != nil {
		t.Fatal(err)
	}
	if e.state(t, id).State != core.TaskClaimed {
		t.Fatal("a cancel on another link cancelled the task")
	}
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCancel, e.link.ID, core.TaskCancelBody{TaskID: id})); err != nil {
		t.Fatal(err)
	}
	if e.state(t, id).State != core.TaskCancelled {
		t.Fatal("task not cancelled")
	}
	items, _ := e.inbox.Check(ctx, e.session.ID, 10)
	if len(items) != 1 || items[0].Kind != "task_update" || !strings.Contains(items[0].Wrapped, "cancelled by sender") {
		t.Fatalf("session notice = %+v", items)
	}
	if _, err := e.tasks.Complete(ctx, e.session.ID, "/w", id, "done anyway", nil); !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("complete after cancel err = %v", err)
	}
}

func TestHandleUpdateMirror(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	other := d2Share(t, e.shared, "other")
	otherLink := d2Link(t, e.st, e.peer, other, "trainer", core.PermMessages, core.PermTasksAuto)
	id, err := e.tasks.Create(ctx, e.session.ID, "/w/proj", e.link.Num, "train", nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(linkID string, body core.TaskUpdateBody) {
		if err := e.handle(t, d2Env(t, e.peer, core.KindTaskUpdate, linkID, body)); err != nil {
			t.Fatal(err)
		}
	}
	send(otherLink.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskDone, Result: "forged"})
	if e.state(t, id).State != core.TaskSent {
		t.Fatal("an update on another link changed our task")
	}
	send(e.link.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskRunning, Note: "epoch 1"})
	send(e.link.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskDone, Result: "loss 0.1"})
	send(e.link.ID, core.TaskUpdateBody{TaskID: id, State: core.TaskClaimed})
	tk := e.state(t, id)
	if tk.State != core.TaskDone || tk.Result != "loss 0.1" || len(tk.Notes) != 1 || tk.ClaimedBy != "trainer" {
		t.Fatalf("mirror = %+v", tk)
	}
	items, _ := e.inbox.Check(ctx, e.session.ID, 10)
	if len(items) != 2 || items[0].Kind != "task_update" || !strings.Contains(items[1].Wrapped, "loss 0.1") {
		t.Fatalf("creator inbox = %+v", items)
	}
	if got, _ := e.inbox.Check(ctx, other.ID, 10); len(got) != 0 {
		t.Fatalf("other session saw %d task updates", len(got))
	}
	if got, err := e.tasks.Get(ctx, e.session.ID, id); err != nil || got.ID != id {
		t.Fatalf("creator Get: %v", err)
	}
	if _, err := e.tasks.Get(ctx, other.ID, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("other session Get err = %v", err)
	}
}

// Agents must not read instructions a human has not approved: held and
// rejected inbound tasks look like they do not exist.
func TestGetHidesUnapprovedInboundTasks(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk)
	msgLink := d2Link(t, e.st, e.peer, e.session, "chatty", core.PermMessages, core.PermMessages)
	autoLink := d2Link(t, e.st, e.peer, e.session, "worker", core.PermTasksAuto, core.PermMessages)
	held := e.incoming(t, "held work")
	create := func(l store.Link, text string) string {
		id := core.NewID()
		if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCreate, l.ID, core.TaskCreateBody{TaskID: id, Instructions: text})); err != nil {
			t.Fatal(err)
		}
		return id
	}
	rejected := create(msgLink, "rejected work")
	queued := create(autoLink, "open work")
	for _, id := range []string{held, rejected} {
		if tk, err := e.tasks.Get(ctx, e.session.ID, id); !errors.Is(err, core.ErrNotFound) || tk.Instructions != "" {
			t.Fatalf("Get(%s) = %+v, %v; want ErrNotFound", e.state(t, id).State, tk, err)
		}
	}
	if tk, err := e.tasks.Get(ctx, e.session.ID, queued); err != nil || tk.Instructions != "open work" {
		t.Fatalf("Get(queued) = %+v, %v", tk, err)
	}
	if err := e.tasks.Decide(ctx, held, true, AuthPassword); err != nil {
		t.Fatal(err)
	}
	if tk, err := e.tasks.Get(ctx, e.session.ID, held); err != nil || tk.Instructions != "held work" {
		t.Fatalf("Get(approved) = %+v, %v", tk, err)
	}
}

// v2 spec 4: the sender learns when the receiving session's inbox first
// returns a task (seen), so a slow session and a stuck one look different.
func TestSeenSentWhenTheInboxReturnsTheTask(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	e.inbox.AddReadObserver(e.tasks)
	id := e.incoming(t, "work")
	seen := func() int {
		n := 0
		for _, u := range d2Updates(t, e.sender) {
			if u.TaskID == id && u.State == core.TaskSeen {
				n++
			}
		}
		return n
	}
	if seen() != 0 {
		t.Fatal("seen sent before the session read its inbox")
	}
	if items, _ := e.inbox.Check(ctx, e.session.ID, 10); len(items) != 1 {
		t.Fatalf("inbox %+v", items)
	}
	if seen() != 1 {
		t.Fatalf("seen sent %d times, want 1", seen())
	}
	e.inbox.Check(ctx, e.session.ID, 10)
	if seen() != 1 {
		t.Fatal("seen sent again")
	}
	if st := e.state(t, id).State; st != core.TaskQueued {
		t.Fatalf("receiver state %s, want queued (seen is sender-side only)", st)
	}
}

func TestSenderMirrorsSeenWithoutGoingBackwards(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	id, err := e.tasks.Create(ctx, e.session.ID, "/w", e.link.Num, "work", nil)
	if err != nil {
		t.Fatal(err)
	}
	send := func(st core.TaskState) {
		if err := e.handle(t, d2Env(t, e.peer, core.KindTaskUpdate, e.link.ID, core.TaskUpdateBody{TaskID: id, State: st})); err != nil {
			t.Fatal(err)
		}
	}
	send(core.TaskQueued)
	send(core.TaskSeen)
	if st := e.state(t, id).State; st != core.TaskSeen {
		t.Fatalf("after seen: %s", st)
	}
	send(core.TaskClaimed)
	send(core.TaskSeen) // a late, reordered seen
	if st := e.state(t, id).State; st != core.TaskClaimed {
		t.Fatalf("a late seen moved the task back to %s", st)
	}
}
