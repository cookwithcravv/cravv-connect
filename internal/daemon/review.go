package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Review limits (v2 spec 7.2).
const (
	ReviewPerMinute = 6
	reviewPreview   = 280 // characters of a held task shown in a code notification
)

// Item ID prefixes: "link-<number>" for a link request, "task-<task id>"
// for a tasks-ask task.
const (
	reviewLinkPrefix = "link-"
	reviewTaskPrefix = "task-"
)

// Errors of the chat review path (the API maps them to busy and not_found).
var ErrReviewRateLimited = fmt.Errorf("review_pending is limited to %d calls a minute: wait a minute and call it again", ReviewPerMinute)

// ReviewItem is one decision waiting for a session's human: a link request
// (Task nil) or a task on a tasks-ask link. Alias is the local alias of the
// peer; the link's remote name is validated; purpose, note and the task's
// instructions are peer text.
type ReviewItem struct {
	ID    string
	Link  store.Link
	Alias string
	Task  *store.Task
}

// ReviewLinks is what ReviewService needs from LinkService.
type ReviewLinks interface {
	DecideVia(ctx context.Context, d Decider, num int64) (store.Link, error)
}

// ReviewTasks is what ReviewService needs from TaskService.
type ReviewTasks interface {
	DecideVia(ctx context.Context, d Decider, id string) (store.Task, error)
}

// ReviewDeps are the ReviewService collaborators.
type ReviewDeps struct {
	Links     store.LinkStore
	Tasks     store.TaskStore
	Peers     store.PeerStore
	LinkSvc   ReviewLinks
	TaskSvc   ReviewTasks
	Codes     *ConfirmCodes
	Clock     core.Clock
	RateLimit *RateLimiter // default ReviewPerMinute per session per minute
}

// ReviewService serves review_pending: the decisions waiting for one
// session's human, applied with AuthChat through a Decider (an elicitation
// answer, or an answer carrying a confirmation code), so the chat can never
// grant tasks-auto or raise a permission.
type ReviewService struct{ d ReviewDeps }

// NewReviewService builds the service.
func NewReviewService(d ReviewDeps) *ReviewService {
	if d.RateLimit == nil {
		d.RateLimit = NewRateLimiter(d.Clock, ReviewPerMinute, time.Minute)
	}
	return &ReviewService{d: d}
}

// Pending lists the session's pending decisions, oldest first. Each call
// counts toward ReviewPerMinute for the session.
func (s *ReviewService) Pending(ctx context.Context, session string) ([]ReviewItem, error) {
	if !s.d.RateLimit.Allow(session) {
		return nil, ErrReviewRateLimited
	}
	return s.pending(ctx, session)
}

func (s *ReviewService) pending(ctx context.Context, session string) ([]ReviewItem, error) {
	reqs, err := s.d.Links.ListLinks(ctx, store.LinkFilter{Session: session, Direction: store.LinkInbound, States: []store.LinkState{store.LinkPending}})
	if err != nil {
		return nil, err
	}
	out := make([]ReviewItem, 0, len(reqs))
	for _, l := range reqs {
		out = append(out, ReviewItem{ID: reviewLinkPrefix + strconv.FormatInt(l.Num, 10), Link: l, Alias: s.alias(ctx, l.Peer)})
	}
	held, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: []core.TaskState{core.TaskAwaitingApproval}})
	if err != nil {
		return nil, err
	}
	for _, t := range held {
		if t.ToSession != session {
			continue
		}
		l, err := s.d.Links.GetLink(ctx, t.Peer, t.LinkID)
		if err != nil {
			continue
		}
		task := t
		out = append(out, ReviewItem{ID: reviewTaskPrefix + t.ID, Link: l, Alias: s.alias(ctx, t.Peer), Task: &task})
	}
	return out, nil
}

// item returns the session's pending item id; any other looks missing.
func (s *ReviewService) item(ctx context.Context, session, id string) (ReviewItem, error) {
	items, err := s.pending(ctx, session)
	if err != nil {
		return ReviewItem{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return ReviewItem{}, fmt.Errorf("%s is not waiting for a decision in this session: %w", id, core.ErrNotFound)
}

// Decide applies the human's answer to the session's item through d with
// AuthChat. It returns the decided link (and task, for a task item).
func (s *ReviewService) Decide(ctx context.Context, session, id string, d Decider) (store.Link, *store.Task, error) {
	it, err := s.item(ctx, session, id)
	if err != nil {
		return store.Link{}, nil, err
	}
	if cd, ok := d.(CodeDecider); ok { // wrong codes count against this session
		cd.Session = session
		d = cd
	}
	if it.Task == nil {
		l, err := s.d.LinkSvc.DecideVia(ctx, d, it.Link.Num)
		if err == nil {
			s.d.Codes.Forget(id)
		}
		return l, nil, err
	}
	t, err := s.d.TaskSvc.DecideVia(ctx, d, it.Task.ID)
	if err != nil {
		return it.Link, nil, err
	}
	s.d.Codes.Forget(id)
	return it.Link, &t, nil
}

// ShowCode shows the item's confirmation code in a desktop notification
// that says what is being decided. The code itself never leaves the daemon
// except to the desktop.
func (s *ReviewService) ShowCode(ctx context.Context, session, id string) error {
	it, err := s.item(ctx, session, id)
	if err != nil {
		return err
	}
	return s.d.Codes.Show(session, id, codeText(it))
}

// codeText describes an item for its code notification.
func codeText(it ReviewItem) string {
	if it.Task == nil {
		return fmt.Sprintf("cravv-connect: link request from %s/%s asking %s. Type accept <code> or reject in the chat.",
			it.Alias, it.Link.RemoteName, it.Link.Proposed)
	}
	preview := strings.Join(strings.Fields(previewText(it.Task.Instructions, reviewPreview)), " ")
	return fmt.Sprintf("cravv-connect: task from %s/%s on link %d waits for approval: %s. Type accept <code> or reject in the chat.",
		it.Alias, it.Link.RemoteName, it.Link.Num, preview)
}

func (s *ReviewService) alias(ctx context.Context, id core.MachineID) string {
	if p, err := s.d.Peers.GetPeer(ctx, id); err == nil {
		return p.Alias
	}
	return id.Short()
}

// AnswerDecider is a human's answer the MCP server got from an
// elicitation form. It is applied with AuthChat.
type AnswerDecider struct{ Answer DecisionAnswer }

// Decide returns the answer.
func (a AnswerDecider) Decide(context.Context, DecisionRequest) (DecisionAnswer, error) {
	return a.Answer, nil
}

// CodeDecider is an answer the human typed in the chat with a
// confirmation code. Accepting needs the item's right code; rejecting
// needs none (lowering never needs a gate). A code accepts a link at most
// at tasks-ask: the chat tier cannot grant tasks-auto.
type CodeDecider struct {
	Codes   *ConfirmCodes
	Session string // set by ReviewService.Decide
	Item    string
	Code    string
	Answer  DecisionAnswer
}

// Decide checks the code and returns the answer.
func (c CodeDecider) Decide(_ context.Context, req DecisionRequest) (DecisionAnswer, error) {
	if !c.Answer.Accept {
		return c.Answer, nil
	}
	if c.Code == "" {
		return DecisionAnswer{}, ErrNoDecision
	}
	if err := c.Codes.Check(c.Session, c.Item, c.Code); err != nil {
		return DecisionAnswer{}, err
	}
	ans := c.Answer
	if req.Task == nil && ans.Permission == "" {
		ans.Permission = core.MinPermission(req.Link.Proposed, core.PermTasksAsk)
	}
	return ans, nil
}

// ErrBadReviewItem is returned for an item ID of the wrong form.
var ErrBadReviewItem = errors.New("invalid item: use link-<number> or task-<task id> from review_pending")

// ValidReviewItem reports whether id has the form of a review item ID.
func ValidReviewItem(id string) error {
	if strings.HasPrefix(id, reviewLinkPrefix) {
		if _, err := strconv.ParseInt(strings.TrimPrefix(id, reviewLinkPrefix), 10, 64); err == nil {
			return nil
		}
	}
	if strings.HasPrefix(id, reviewTaskPrefix) && core.ValidID(strings.TrimPrefix(id, reviewTaskPrefix)) {
		return nil
	}
	return ErrBadReviewItem
}
