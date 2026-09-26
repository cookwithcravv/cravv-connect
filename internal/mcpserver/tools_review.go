package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// review_pending (v2 spec 7.2) asks the human behind this chat to decide
// link requests and tasks on tasks-ask links, outside the model's control:
// one elicitation form per item, answered by the human in the client. Only
// a real answer counts: action "accept" with a decision the form offered.
// A decline, a cancel or a client that cannot show forms leaves the item
// pending and falls back to a confirmation code the daemon shows on the
// desktop, which the human types in the chat. Either way the daemon applies
// the answer at the chat tier, which can never grant tasks-auto.

type reviewPendingTool struct{}

type reviewIn struct {
	Item     string `json:"item,omitempty" jsonschema:"an item from an earlier review_pending result, to pass on what the human typed"`
	Decision string `json:"decision,omitempty" jsonschema:"what the human typed: accept or reject"`
	Code     string `json:"code,omitempty" jsonschema:"the 4-digit code the human typed after accept (from their desktop notification)"`
}

// Form choices. A link request offers accepting at the level asked (never
// above tasks-ask), accepting lower, and rejecting; a task, approving or
// denying.
const (
	choiceAccept      = "accept"
	choiceAsTasksAsk  = "accept as tasks-ask"
	choiceAsMessages  = "accept as messages"
	choiceReject      = "reject"
	decisionFieldName = "decision"
)

// webUI names the web UI, where the human decides with their password
// like in a terminal (v2 spec 7.2: the form and the fallback name both).
const webUI = "web UI (cravv-connect ui)"

// reviewAnswer is a human's answer to one item's form.
type reviewAnswer struct {
	accept     bool
	permission string
}

// reviewer holds this chat's open forms and answers not yet applied.
type reviewer struct {
	c Caller

	mu      sync.Mutex
	open    map[string]bool         // items with a form on screen
	answers map[string]reviewAnswer // answered but not yet applied (single use)
}

func (reviewPendingTool) Register(s *mcp.Server, c Caller) {
	r := &reviewer{c: c, open: map[string]bool{}, answers: map[string]reviewAnswer{}}
	mcp.AddTool(s, &mcp.Tool{
		Name: "review_pending",
		Description: "Ask your human to decide link requests and tasks waiting on tasks-ask links: one form each. " +
			"If the form cannot be shown, the human gets a 4-digit code in a desktop notification; when they type " +
			"\"accept <code>\" or \"reject\", call review_pending again with item, decision and code. You never see the code.",
		Annotations: annLocal,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in reviewIn) (*mcp.CallToolResult, any, error) {
		var text string
		var err error
		if in.Item != "" || in.Decision != "" || in.Code != "" {
			text, err = r.typed(ctx, in)
		} else {
			text, err = r.review(ctx, req.Session)
		}
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})
}

// typed applies what the human typed in the chat.
func (r *reviewer) typed(ctx context.Context, in reviewIn) (string, error) {
	if in.Item == "" {
		return "", errors.New("give the item from the review_pending result")
	}
	p := ipc.ReviewDecideParams{Item: in.Item}
	switch strings.ToLower(strings.TrimSpace(in.Decision)) {
	case "reject":
	case "accept":
		code := strings.TrimSpace(in.Code)
		if code == "" {
			return "", errors.New("accepting needs the 4-digit code the human typed after accept; ask them to read it from the cravv-connect notification")
		}
		p.Accept, p.Code = true, code
	default:
		return "", errors.New("decision must be accept or reject, as the human typed it")
	}
	return r.decide(ctx, p)
}

// review lists the pending items and asks the human about each.
func (r *reviewer) review(ctx context.Context, ss *mcp.ServerSession) (string, error) {
	var list ipc.ReviewListResult
	if err := r.c.Call(ctx, ipc.MethodReviewList, nil, &list); err != nil {
		return "", err
	}
	if len(list.Items) == 0 {
		return "Nothing is waiting for a decision.", nil
	}
	lines := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		lines = append(lines, r.one(ctx, ss, it))
	}
	return strings.Join(lines, "\n"), nil
}

// one handles one item and returns a line for the model.
func (r *reviewer) one(ctx context.Context, ss *mcp.ServerSession, it ipc.ReviewItemView) string {
	if ans, ok := r.take(it.Item); ok {
		return r.apply(ctx, it, ans)
	}
	if !r.claim(it.Item) {
		return fmt.Sprintf("%s: a form for it is already open; wait for the human.", it.Item)
	}
	ans, answered := r.ask(ctx, ss, it)
	r.release(it.Item)
	if answered {
		return r.apply(ctx, it, ans)
	}
	return r.fallback(ctx, it)
}

// ask shows the item's form. It reports a decision only for a real
// answer: action accept with one of the offered choices.
func (r *reviewer) ask(ctx context.Context, ss *mcp.ServerSession, it ipc.ReviewItemView) (reviewAnswer, bool) {
	if ss == nil {
		return reviewAnswer{}, false
	}
	message, choices := formFor(it)
	res, err := ss.Elicit(ctx, &mcp.ElicitParams{
		Message: message,
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				decisionFieldName: map[string]any{"type": "string", "title": "Your decision", "enum": choices},
			},
			"required": []string{decisionFieldName},
		},
	})
	if err != nil || res == nil || res.Action != "accept" {
		return reviewAnswer{}, false
	}
	choice, _ := res.Content[decisionFieldName].(string)
	if !slices.Contains(choices, choice) {
		return reviewAnswer{}, false
	}
	return answerFor(it, choice), true
}

// formFor builds the form text shown to the human and its choices. The
// peer's text (purpose, note, task instructions) is shown only here,
// wrapped, never to the model.
func formFor(it ipc.ReviewItemView) (string, []string) {
	var b strings.Builder
	if it.Kind == "task" {
		fmt.Fprintf(&b, "cravv-connect: %s/%s sent a task on link %d that needs your approval before this chat sees it.", it.Machine, it.Session, it.Link)
		if it.Wrapped != "" {
			fmt.Fprintf(&b, "\nThe task, written by the other machine:\n%s", it.Wrapped)
		}
		return b.String(), []string{choiceAccept, choiceReject}
	}
	fmt.Fprintf(&b, "cravv-connect: %s/%s asks to link with this chat's session with permission %s (link %d).", it.Machine, it.Session, it.Permission, it.Link)
	if it.Wrapped != "" {
		fmt.Fprintf(&b, "\nWritten by the other machine:\n%s", it.Wrapped)
	}
	switch it.Permission {
	case "tasks-auto":
		fmt.Fprintf(&b, "\nGranting tasks-auto needs your password: run cravv-connect link accept %d in a terminal, or open the %s. Here you can accept it lower.", it.Link, webUI)
		return b.String(), []string{choiceAsTasksAsk, choiceAsMessages, choiceReject}
	case "tasks-ask":
		return b.String(), []string{choiceAccept, choiceAsMessages, choiceReject}
	}
	return b.String(), []string{choiceAccept, choiceReject}
}

func answerFor(it ipc.ReviewItemView, choice string) reviewAnswer {
	switch choice {
	case choiceAccept:
		if it.Kind == "task" {
			return reviewAnswer{accept: true}
		}
		return reviewAnswer{accept: true, permission: it.Permission}
	case choiceAsTasksAsk:
		return reviewAnswer{accept: true, permission: "tasks-ask"}
	case choiceAsMessages:
		return reviewAnswer{accept: true, permission: "messages"}
	}
	return reviewAnswer{}
}

// apply sends an answer to the daemon. It is cached first, so an answer
// the daemon did not get (a dropped connection) is applied on the next
// call without asking again, and dropped once applied: answers are single
// use.
func (r *reviewer) apply(ctx context.Context, it ipc.ReviewItemView, ans reviewAnswer) string {
	r.mu.Lock()
	r.answers[it.Item] = ans
	r.mu.Unlock()
	line, err := r.decide(ctx, ipc.ReviewDecideParams{Item: it.Item, Accept: ans.accept, Permission: ans.permission})
	if err != nil && errors.Is(err, ipc.ErrClosed) {
		return fmt.Sprintf("%s: the human answered but the daemon did not get it; call review_pending again.", it.Item)
	}
	r.take(it.Item)
	if err != nil {
		return fmt.Sprintf("%s: %v", it.Item, err)
	}
	return line
}

// decide calls review.decide and describes the outcome.
func (r *reviewer) decide(ctx context.Context, p ipc.ReviewDecideParams) (string, error) {
	var res ipc.ReviewDecideResult
	err := r.c.Call(ctx, ipc.MethodReviewDecide, p, &res)
	switch {
	case ipc.IsKind(err, ipc.KindBadCode):
		return "", fmt.Errorf("%s: that code is wrong or expired. Ask the human to read the newest cravv-connect notification; after %d wrong codes the item can only be decided with the human's password", p.Item, 3)
	case ipc.IsKind(err, ipc.KindCodeLocked):
		return "", fmt.Errorf("%s: too many wrong codes, so no code works for it any more. The human decides with their password: in a terminal (cravv-connect links, cravv-connect approvals) or the %s", p.Item, webUI)
	case err != nil:
		return "", err
	}
	switch res.Outcome {
	case "accepted":
		return fmt.Sprintf("%s: accepted; link %d now lets the other side do %s.", p.Item, res.Link, res.Permission), nil
	case "approved":
		return fmt.Sprintf("%s: approved by the human; the task is in check_inbox now. Do not ask again.", p.Item), nil
	case "denied":
		return fmt.Sprintf("%s: denied; the sender is told.", p.Item), nil
	}
	return fmt.Sprintf("%s: rejected; the other side is told.", p.Item), nil
}

// fallback shows the item's confirmation code on the desktop, or explains
// the terminal path when this machine cannot show notifications.
func (r *reviewer) fallback(ctx context.Context, it ipc.ReviewItemView) string {
	err := r.c.Call(ctx, ipc.MethodReviewCode, ipc.ReviewItemParams{Item: it.Item}, nil)
	switch {
	case err == nil:
		return fmt.Sprintf("%s (%s from %s/%s): no answer from a form. A cravv-connect notification on this machine shows a 4-digit code. "+
			"Ask the human to type \"accept <code>\" or \"reject\"; then call review_pending with item %q, decision and code.",
			it.Item, describe(it), it.Machine, it.Session, it.Item)
	case ipc.IsKind(err, ipc.KindNoDesktop):
		return fmt.Sprintf("%s (%s from %s/%s): this machine cannot show a form or a notification. The human decides with their password: %s.",
			it.Item, describe(it), it.Machine, it.Session, passwordPath(it))
	case ipc.IsKind(err, ipc.KindCodeLocked):
		return fmt.Sprintf("%s: too many wrong codes, so no code works for it any more; the human decides with their password (%s).", it.Item, passwordPath(it))
	}
	return fmt.Sprintf("%s: %v", it.Item, err)
}

func describe(it ipc.ReviewItemView) string {
	if it.Kind == "task" {
		return fmt.Sprintf("a task on link %d", it.Link)
	}
	return fmt.Sprintf("a link request asking %s", it.Permission)
}

// passwordPath is where the human decides it with their password: the
// terminal command or the web UI's Approvals page.
func passwordPath(it ipc.ReviewItemView) string {
	if it.Kind == "task" {
		return "cravv-connect approvals in a terminal, or the Approvals page of the " + webUI
	}
	return fmt.Sprintf("cravv-connect link accept %d (or cravv-connect link reject %d) in a terminal, or the Approvals page of the %s", it.Link, it.Link, webUI)
}

// claim marks a form open for item; false if one already is.
func (r *reviewer) claim(item string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open[item] {
		return false
	}
	r.open[item] = true
	return true
}

func (r *reviewer) release(item string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.open, item)
}

// take removes and returns a cached answer.
func (r *reviewer) take(item string) (reviewAnswer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ans, ok := r.answers[item]
	delete(r.answers, item)
	return ans, ok
}
