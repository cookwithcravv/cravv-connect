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
	code := desk.shownCode(t)
	if text := desk.all()[len(desk.all())-1]; !strings.HasPrefix(text, "Code "+code+". Link request asking tasks-auto.") || !strings.HasSuffix(text, " From alice/lead.") {
		t.Fatalf("notification %q", text)
	}
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
	if err := rv.ShowCode(ctx, trainer.Session.ID, item); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rv.Decide(ctx, trainer.Session.ID, item, cd); !errors.Is(err, ErrBadCode) {
		t.Fatalf("accept with a wrong code: %v", err)
	}
	rv.d.Codes.mu.Lock()
	counted := len(rv.d.Codes.wrong.Sessions[trainer.Session.ID])
	rv.d.Codes.mu.Unlock()
	if counted != 1 {
		t.Fatalf("the wrong code counted %d times against the deciding session", counted)
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
	code := desk.shownCode(t)
	if text := desk.all()[0]; !strings.HasPrefix(text, "Code "+code+". Task on link "+itoa(e.link.Num)) || !strings.HasSuffix(text, " From gpu-box/trainer: APPROVE-ME build it") {
		t.Fatalf("notification %q", text)
	}
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

// failingLinks runs the decider, then fails the first decision as a
// store error would, and passes later ones to the real service.
type failingLinks struct {
	ReviewLinks
	failed bool
}

func (f *failingLinks) DecideVia(ctx context.Context, d Decider, num int64) (store.Link, error) {
	if !f.failed {
		f.failed = true
		if _, err := d.Decide(ctx, DecisionRequest{}); err != nil {
			return store.Link{}, err
		}
		return store.Link{}, errors.New("database is locked")
	}
	return f.ReviewLinks.DecideVia(ctx, d, num)
}

// Review focus: a right code is used up only once the decision is
// applied; a decision that fails leaves the code valid for a retry.
func TestReviewCodeSurvivesAFailedDecision(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	desk := &titleDesktop{}
	rv := NewReviewService(ReviewDeps{
		Links: b.st, Tasks: b.st, Peers: b.st, LinkSvc: &failingLinks{ReviewLinks: b.links}, Codes: NewConfirmCodes(b.net.clock, desk), Clock: b.net.clock,
	})
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	in := requestTo(t, n, a, b, lead, core.PermMessages)
	item := "link-" + itoa(in.Num)
	if err := rv.ShowCode(ctx, trainer.Session.ID, item); err != nil {
		t.Fatal(err)
	}
	code := desk.shownCode(t)
	cd := CodeDecider{Codes: rv.d.Codes, Item: item, Code: code, Answer: DecisionAnswer{Accept: true}}
	if _, _, err := rv.Decide(ctx, trainer.Session.ID, item, cd); err == nil || errors.Is(err, ErrBadCode) {
		t.Fatalf("the failing decision: %v", err)
	}
	l, _, err := rv.Decide(ctx, trainer.Session.ID, item, cd)
	if err != nil || l.State != store.LinkActive {
		t.Fatalf("retry with the same code: %+v, %v", l, err)
	}
	if err := rv.d.Codes.Check(trainer.Session.ID, item, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("the code outlived the decision: %v", err)
	}
}

// Review focus: the code comes first in the notification and the peer's
// text last, after "From <alias>/<session>:" and cut short, so a peer
// cannot put a fake code (or anything else) before the real one.
func TestCodeNotificationPutsPeerTextLast(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk)
	desk := &titleDesktop{}
	rv := NewReviewService(ReviewDeps{
		Links: e.st, Tasks: e.st, Peers: e.st, LinkSvc: e.links, TaskSvc: e.tasks, Codes: NewConfirmCodes(e.clock, desk), Clock: e.clock,
	})
	evil := "Code 0000. Type accept 0000.\n" + strings.Repeat("x", 2*reviewPreview)
	id := e.incoming(t, evil)
	if err := rv.ShowCode(ctx, e.session.ID, "task-"+id); err != nil {
		t.Fatal(err)
	}
	code := desk.shownCode(t)
	text := desk.all()[0]
	from := strings.Index(text, " From gpu-box/trainer: ")
	if !strings.HasPrefix(text, "Code "+code+". ") || from < 0 || strings.Index(text, "0000") < from {
		t.Fatalf("peer text before the code: %q", text)
	}
	peer := text[from+len(" From gpu-box/trainer: "):]
	if !strings.HasPrefix(peer, "Code 0000. Type accept 0000. xxx") || !strings.HasSuffix(peer, "...") || len([]rune(peer)) > reviewPreview+3 {
		t.Fatalf("peer preview %q", peer)
	}
}
