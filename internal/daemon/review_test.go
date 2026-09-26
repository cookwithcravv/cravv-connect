package daemon

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// reviewOn builds a ReviewService for node v's links (no tasks).
func reviewOn(v *v2Node, desk DesktopNotifier) *ReviewService {
	return NewReviewService(ReviewDeps{
		Links: v.st, Tasks: v.st, Peers: v.st, LinkSvc: v.links, Codes: NewConfirmCodes(v.net.clock, desk), Clock: v.net.clock,
	})
}

// requestTo has alice's lead ask bob's trainer for a link at perm and
// returns bob's side of it.
func requestTo(t *testing.T, n *v2Net, a, b *v2Node, lead Shared, perm core.Permission) store.Link {
	t.Helper()
	out, err := a.links.Connect(context.Background(), lead.Session.ID, "bob/trainer", perm, "please")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	return b.linkOf(t, a, out.ID)
}

// Review focus: a chat decision (elicitation or confirmation code) can
// never grant tasks-auto; the code path accepts at most at tasks-ask.
func TestChatDecisionsCannotGrantTasksAuto(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	desk := &titleDesktop{}
	rv := reviewOn(b, desk)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	in := requestTo(t, n, a, b, lead, core.PermTasksAuto)
	item := "link-" + itoa(in.Num)

	items, err := rv.Pending(ctx, trainer.Session.ID)
	if err != nil || len(items) != 1 || items[0].ID != item || items[0].Alias != "alice" || items[0].Task != nil {
		t.Fatalf("pending %+v, %v", items, err)
	}
	for _, perm := range []core.Permission{"", core.PermTasksAuto} {
		_, _, err := rv.Decide(ctx, trainer.Session.ID, item, AnswerDecider{DecisionAnswer{Accept: true, Permission: perm}})
		if !errors.Is(err, core.ErrAuthRequired) {
			t.Fatalf("elicitation accept at %q granted tasks-auto: %v", perm, err)
		}
	}
	if err := rv.ShowCode(ctx, trainer.Session.ID, item); err != nil {
		t.Fatal(err)
	}
	if text := desk.all()[len(desk.all())-1]; !strings.Contains(text, "link request from alice/lead asking tasks-auto") {
		t.Fatalf("notification %q", text)
	}
	code := desk.shownCode(t)
	l, _, err := rv.Decide(ctx, trainer.Session.ID, item, CodeDecider{Codes: rv.d.Codes, Item: item, Code: code, Answer: DecisionAnswer{Accept: true}})
	if err != nil || l.State != store.LinkActive || l.PermissionIn != core.PermTasksAsk {
		t.Fatalf("code accept: %+v, %v", l, err)
	}
	if _, err := b.links.SetPermission(ctx, trainer.Session.ID, l.Num, core.PermTasksAuto, AuthChat); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("a chat raise: %v", err)
	}
}

func TestReviewIsScopedAndRateLimited(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	rv := reviewOn(b, &titleDesktop{})
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	other := shareOn(t, b, 2, "other", core.Visibility{})
	in := requestTo(t, n, a, b, lead, core.PermMessages)
	item := "link-" + itoa(in.Num)

	if items, err := rv.Pending(ctx, other.Session.ID); err != nil || len(items) != 0 {
		t.Fatalf("another session sees %+v, %v", items, err)
	}
	if _, _, err := rv.Decide(ctx, other.Session.ID, item, AnswerDecider{DecisionAnswer{Accept: true}}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session decided: %v", err)
	}
	if err := rv.ShowCode(ctx, other.Session.ID, item); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("another session got a code shown: %v", err)
	}
	for i := range ReviewPerMinute {
		if _, err := rv.Pending(ctx, trainer.Session.ID); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := rv.Pending(ctx, trainer.Session.ID); !errors.Is(err, ErrReviewRateLimited) {
		t.Fatalf("7th call in a minute: %v", err)
	}
	b.net.clock.Advance(time.Minute)
	if items, err := rv.Pending(ctx, trainer.Session.ID); err != nil || len(items) != 1 {
		t.Fatalf("after a minute: %+v, %v", items, err)
	}
	// Accepting needs a real answer: no code means no decision.
	cd := CodeDecider{Codes: rv.d.Codes, Item: item, Answer: DecisionAnswer{Accept: true}}
	if _, _, err := rv.Decide(ctx, trainer.Session.ID, item, cd); !errors.Is(err, ErrNoDecision) {
		t.Fatalf("accept without a code: %v", err)
	}
	cd.Code = "0000"
	if _, _, err := rv.Decide(ctx, trainer.Session.ID, item, cd); !errors.Is(err, ErrBadCode) {
		t.Fatalf("accept with a code never shown: %v", err)
	}
	if got := b.linkOf(t, a, in.ID); got.State != store.LinkPending {
		t.Fatalf("still pending, got %s", got.State)
	}
	// Rejecting needs no code.
	l, _, err := rv.Decide(ctx, trainer.Session.ID, item, CodeDecider{Codes: rv.d.Codes, Item: item})
	if err != nil || l.State != store.LinkClosed {
		t.Fatalf("reject: %+v, %v", l, err)
	}
	if err := ValidReviewItem(item); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "link-", "link-x", "task-nope", "3"} {
		if err := ValidReviewItem(bad); !errors.Is(err, ErrBadReviewItem) {
			t.Errorf("ValidReviewItem(%q) = %v", bad, err)
		}
	}
}

func TestReviewApprovesTasksAskTasks(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk)
	desk := &titleDesktop{}
	rv := NewReviewService(ReviewDeps{
		Links: e.st, Tasks: e.st, Peers: e.st, LinkSvc: e.links, TaskSvc: e.tasks, Codes: NewConfirmCodes(e.clock, desk), Clock: e.clock,
	})
	approve := e.incoming(t, "APPROVE-ME build it")
	e.clock.Advance(time.Second) // oldest first
	deny := e.incoming(t, "DENY-ME wipe it")
	items, err := rv.Pending(ctx, e.session.ID)
	if err != nil || len(items) != 2 || items[0].ID != "task-"+approve || items[0].Task == nil || items[0].Link.Num != e.link.Num {
		t.Fatalf("pending %+v, %v", items, err)
	}
	if err := rv.ShowCode(ctx, e.session.ID, "task-"+approve); err != nil {
		t.Fatal(err)
	}
	if text := desk.all()[0]; !strings.Contains(text, "task from gpu-box/trainer on link") || !strings.Contains(text, "APPROVE-ME build it") {
		t.Fatalf("notification %q", text)
	}
	code := desk.shownCode(t)
	_, tk, err := rv.Decide(ctx, e.session.ID, "task-"+approve, CodeDecider{Codes: rv.d.Codes, Item: "task-" + approve, Code: code, Answer: DecisionAnswer{Accept: true}})
	if err != nil || tk == nil || tk.State != core.TaskQueued {
		t.Fatalf("approve: %+v, %v", tk, err)
	}
	_, tk, err = rv.Decide(ctx, e.session.ID, "task-"+deny, AnswerDecider{DecisionAnswer{Accept: false}})
	if err != nil || tk.State != core.TaskRejected {
		t.Fatalf("deny: %+v, %v", tk, err)
	}
	var delivered []string
	items2, _ := e.inbox.Check(ctx, e.session.ID, 10)
	for _, it := range items2 {
		if it.Kind == "task" {
			delivered = append(delivered, it.Item.TaskID)
		}
	}
	if len(delivered) != 1 || delivered[0] != approve {
		t.Fatalf("delivered %v", delivered)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
