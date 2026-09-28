package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// limitedChat is an inboxEnv whose chat handler sits behind an InboundLimiter.
type limitedChat struct {
	*inboxEnv
	clock   *core.FakeClock
	audit   *d2Audit
	limiter *InboundLimiter
	handler Handler
}

func newLimitedChat(t *testing.T, limits InboundLimits) *limitedChat {
	t.Helper()
	clock := core.NewFakeClock(d2Epoch)
	e := &inboxEnv{st: d2Store(t), replies: &d2Replies{}}
	e.shared, e.inbox = d2Inbox(t, e.st, clock)
	e.peer, _ = d2Peer(t, e.st, "gpu-box")
	e.session = d2Share(t, e.shared, "lead")
	e.link = d2Link(t, e.st, e.peer, e.session, "trainer", core.PermTasksAuto, core.PermMessages)
	lc := &limitedChat{inboxEnv: e, clock: clock, audit: &d2Audit{}}
	lc.limiter = NewInboundLimiter(e.inbox, limits, clock, lc.audit)
	lc.handler = d2Gated(e.st, e.shared, e.replies, lc.limiter.Wrap(NewChatHandler(e.inbox)), nil)
	return lc
}

func (lc *limitedChat) send(t *testing.T, text string) error {
	t.Helper()
	env := d2Env(t, lc.peer, core.KindChat, lc.link.ID, core.ChatBody{Text: text})
	return lc.handler.Handle(context.Background(), lc.peer, env)
}

func (lc *limitedChat) unread(t *testing.T) int {
	t.Helper()
	n, _, err := lc.inbox.LinkUnread(context.Background(), lc.session.ID, lc.link.ID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A link that sends faster than the bucket refills gets its extra items
// dropped: not stored, not retried (so Inbound confirms them), audited once
// per minute, and named in status.
func TestInboundLimiterRate(t *testing.T) {
	lc := newLimitedChat(t, InboundLimits{PerMinute: 6, Burst: 3, UnreadItems: 100, UnreadBytes: 1 << 20})
	for i := range 3 {
		if err := lc.send(t, "hi"); err != nil {
			t.Fatalf("item %d within the burst: %v", i, err)
		}
	}
	for range 2 {
		err := lc.send(t, "too fast")
		var re *RetryableError
		if !errors.Is(err, ErrInboundFlood) || errors.As(err, &re) {
			t.Fatalf("over the burst err = %v, want a final ErrInboundFlood", err)
		}
	}
	if n := lc.unread(t); n != 3 {
		t.Fatalf("stored %d items, want 3", n)
	}
	ev := lc.audit.ofType(audit.EvInboundDropped)
	if len(ev) != 1 || ev[0].Alias != "gpu-box" || ev[0].Detail["reason"] != "rate" {
		t.Fatalf("audit = %+v, want one inbound_dropped for gpu-box", ev)
	}
	warn := lc.limiter.Warnings()
	want := "link 1: gpu-box is sending too fast, dropped 2 messages"
	if len(warn) != 1 || warn[0] != want {
		t.Fatalf("warnings = %q, want %q", warn, want)
	}

	// Ten seconds refill one item (6 a minute).
	lc.clock.Advance(10 * time.Second)
	if err := lc.send(t, "later"); err != nil {
		t.Fatalf("after a refill: %v", err)
	}
	if err := lc.send(t, "again too fast"); !errors.Is(err, ErrInboundFlood) {
		t.Fatalf("err = %v, want ErrInboundFlood", err)
	}
	// A minute on, the next drop is audited again; an hour on, status is quiet.
	lc.clock.Advance(time.Minute)
	for range 4 {
		_ = lc.send(t, "burst")
	}
	if ev := lc.audit.ofType(audit.EvInboundDropped); len(ev) != 2 {
		t.Fatalf("%d audit entries, want 2", len(ev))
	}
	lc.clock.Advance(inboundDropWarnFor)
	if warn := lc.limiter.Warnings(); len(warn) != 0 {
		t.Fatalf("warnings an hour later = %q", warn)
	}
}

// Items the session has not read count against the link's budget, in
// items and in bytes; reading frees it.
func TestInboundLimiterUnreadBudget(t *testing.T) {
	ctx := context.Background()
	lc := newLimitedChat(t, InboundLimits{PerMinute: 1000, Burst: 1000, UnreadItems: 3, UnreadBytes: 1 << 20})
	for range 3 {
		if err := lc.send(t, "hi"); err != nil {
			t.Fatal(err)
		}
	}
	if err := lc.send(t, "one more"); !errors.Is(err, ErrInboundFlood) {
		t.Fatalf("over the unread items err = %v", err)
	}
	if got, err := lc.inbox.Check(ctx, lc.session.ID, 10); err != nil || len(got) != 3 {
		t.Fatalf("check = %d, %v", len(got), err)
	}
	if err := lc.send(t, "after reading"); err != nil {
		t.Fatalf("after the session read: %v", err)
	}

	bytes := newLimitedChat(t, InboundLimits{PerMinute: 1000, Burst: 1000, UnreadItems: 100, UnreadBytes: 4 << 10})
	big := strings.Repeat("x", 3<<10)
	if err := bytes.send(t, big); err != nil {
		t.Fatal(err)
	}
	if err := bytes.send(t, big); !errors.Is(err, ErrInboundFlood) {
		t.Fatalf("over the unread bytes err = %v", err)
	}
	if ev := bytes.audit.ofType(audit.EvInboundDropped); len(ev) != 1 || ev[0].Detail["reason"] != "unread" {
		t.Fatalf("audit = %+v", ev)
	}
}

// task.update passes the same limiter (registerHandlers wraps it).
func TestInboundLimiterTaskUpdates(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAuto)
	limiter := NewInboundLimiter(e.inbox, InboundLimits{PerMinute: 1, Burst: 2, UnreadItems: 100, UnreadBytes: 1 << 20}, e.clock, e.audit)
	id, err := e.tasks.Create(ctx, e.session.ID, "", e.link.Num, "train it", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := d2Gated(e.st, e.shared, e.replies, limiter.Wrap(HandlerFunc(e.tasks.HandleUpdate)), nil)
	var errs []error
	for _, st := range []core.TaskState{core.TaskSeen, core.TaskClaimed, core.TaskRunning} {
		env := d2Env(t, e.peer, core.KindTaskUpdate, e.link.ID, core.TaskUpdateBody{TaskID: id, State: st, Note: "progress"})
		errs = append(errs, h.Handle(ctx, e.peer, env))
	}
	if errs[0] != nil || errs[1] != nil || !errors.Is(errs[2], ErrInboundFlood) {
		t.Fatalf("errs = %v, want nil, nil, ErrInboundFlood", errs)
	}
	if got, _ := e.st.GetTask(ctx, id); got.State != core.TaskClaimed {
		t.Fatalf("task state %s, want claimed (the dropped update is not applied)", got.State)
	}
	if n, _, _ := e.inbox.LinkUnread(ctx, e.session.ID, e.link.ID); n != 2 {
		t.Fatalf("%d update items stored, want 2", n)
	}
}

var _ LinkUnreadCounter = (*InboxService)(nil)
