package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Stand-ins for the daemon's errors of these kinds (internal/app registers
// the real ones in the daemon process).
var (
	errTestNoDesktop = errors.New("this machine cannot show desktop notifications")
	errTestBadCode   = errors.New("wrong or expired confirmation code")
)

func init() {
	ipc.RegisterErrorKind(errTestNoDesktop, ipc.KindNoDesktop)
	ipc.RegisterErrorKind(errTestBadCode, ipc.KindBadCode)
}

// Claude Code negotiates a protocol before 2026-07-28, on which a server
// may elicit during a tool call (Phase 0 used this path).
const claudeProto = "2025-11-25"

// reviewDaemon fakes the review methods and records the decisions and
// code requests it gets.
type reviewDaemon struct {
	*daemonFake
	mu      sync.Mutex
	items   []ipc.ReviewItemView
	decided []ipc.ReviewDecideParams
	codes   []string
	codeErr error
	decErr  error
}

func newReviewDaemon(t *testing.T, items ...ipc.ReviewItemView) *reviewDaemon {
	r := &reviewDaemon{daemonFake: newDaemonFake(t), items: items}
	r.handle(ipc.MethodReviewList, ipc.GateSession, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.ReviewListResult{Items: r.items}, nil
	})
	r.handle(ipc.MethodReviewDecide, ipc.GateSession, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.ReviewDecideParams
		json.Unmarshal(raw, &p)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.decided = append(r.decided, p)
		if r.decErr != nil {
			return nil, r.decErr
		}
		out := ipc.ReviewDecideResult{Item: p.Item, Outcome: "rejected", Link: 3}
		if p.Accept {
			out.Outcome, out.Permission = "accepted", p.Permission
		}
		return out, nil
	})
	r.handle(ipc.MethodReviewCode, ipc.GateSession, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.ReviewItemParams
		json.Unmarshal(raw, &p)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.codes = append(r.codes, p.Item)
		return nil, r.codeErr
	})
	r.start()
	return r
}

func (r *reviewDaemon) got() ([]ipc.ReviewDecideParams, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.decided), slices.Clone(r.codes)
}

// human is an elicitation-capable client that answers every form with
// result and records the forms it was shown.
type human struct {
	mu     sync.Mutex
	forms  []*mcp.ElicitParams
	answer func(*mcp.ElicitParams) (*mcp.ElicitResult, error)
}

func (h *human) opts() *mcp.ClientOptions {
	return &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		h.mu.Lock()
		h.forms = append(h.forms, req.Params)
		h.mu.Unlock()
		return h.answer(req.Params)
	}}
}

func (h *human) shown() []*mcp.ElicitParams {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.forms)
}

func answering(action string, content map[string]any) func(*mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return func(*mcp.ElicitParams) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: action, Content: content}, nil
	}
}

func choicesOf(t *testing.T, p *mcp.ElicitParams) []string {
	t.Helper()
	b, _ := json.Marshal(p.RequestedSchema)
	var s struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s.Properties["decision"].Enum
}

var (
	askItem  = ipc.ReviewItemView{Item: "link-3", Kind: "link", Link: 3, Machine: "gpu-box", Session: "trainer", Permission: "tasks-ask", Wrapped: "<remote_message>note: train it</remote_message>"}
	autoItem = ipc.ReviewItemView{Item: "link-4", Kind: "link", Link: 4, Machine: "gpu-box", Session: "trainer", Permission: "tasks-auto"}
	taskItem = ipc.ReviewItemView{Item: "task-T1", Kind: "task", Link: 2, Machine: "gpu-box", Session: "trainer", Permission: "tasks-ask", Wrapped: "<remote_message>SECRET-TASK wipe the disk</remote_message>"}
)

// Only a real answer counts: action accept with an offered choice.
func TestReviewPendingElicitationAnswers(t *testing.T) {
	cases := []struct {
		name     string
		answer   func(*mcp.ElicitParams) (*mcp.ElicitResult, error)
		decided  []ipc.ReviewDecideParams
		codes    []string
		contains string
	}{
		{"accept", answering("accept", map[string]any{"decision": "accept"}),
			[]ipc.ReviewDecideParams{{Item: "link-3", Accept: true, Permission: "tasks-ask"}}, nil, "link-3: accepted"},
		{"accept lower", answering("accept", map[string]any{"decision": "accept as messages"}),
			[]ipc.ReviewDecideParams{{Item: "link-3", Accept: true, Permission: "messages"}}, nil, "lets the other side do messages"},
		{"reject", answering("accept", map[string]any{"decision": "reject"}),
			[]ipc.ReviewDecideParams{{Item: "link-3"}}, nil, "link-3: rejected"},
		{"decline is not a decision", answering("decline", nil), nil, []string{"link-3"}, "4-digit code"},
		{"cancel is not a decision", answering("cancel", nil), nil, []string{"link-3"}, "4-digit code"},
		{"accept without a decision", answering("accept", map[string]any{}), nil, []string{"link-3"}, "4-digit code"},
		{"a choice the form did not offer", answering("accept", map[string]any{"decision": "accept as tasks-auto"}), nil, []string{"link-3"}, "4-digit code"},
		{"client error", func(*mcp.ElicitParams) (*mcp.ElicitResult, error) { return nil, fmt.Errorf("no ui") }, nil, []string{"link-3"}, "4-digit code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newReviewDaemon(t, askItem)
			h := &human{answer: tc.answer}
			cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, h.opts(), claudeProto)
			text, isErr := callTool(t, cs, "review_pending", nil)
			decided, codes := d.got()
			if isErr || !strings.Contains(text, tc.contains) || !slices.Equal(decided, tc.decided) || !slices.Equal(codes, tc.codes) {
				t.Fatalf("text %q (err %v)\ndecided %+v\ncodes %v", text, isErr, decided, codes)
			}
			if forms := h.shown(); len(forms) != 1 || !strings.Contains(forms[0].Message, "gpu-box/trainer asks to link") ||
				!strings.Contains(forms[0].Message, "note: train it") {
				t.Fatalf("forms %+v", forms)
			}
		})
	}
}

// Review focus: a form never offers tasks-auto (the chat tier cannot grant
// it), and a task's instructions reach only the human's form, never the
// model.
func TestReviewPendingTierAndTaskText(t *testing.T) {
	d := newReviewDaemon(t, autoItem, taskItem)
	h := &human{answer: func(p *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		if strings.Contains(p.Message, "sent a task") {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "accept"}}, nil
		}
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "accept as tasks-ask"}}, nil
	}}
	cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, h.opts(), claudeProto)
	text, isErr := callTool(t, cs, "review_pending", nil)
	if isErr || strings.Contains(text, "SECRET-TASK") {
		t.Fatalf("model saw %q", text)
	}
	forms := h.shown()
	if len(forms) != 2 {
		t.Fatalf("forms %d", len(forms))
	}
	if c := choicesOf(t, forms[0]); !slices.Equal(c, []string{"accept as tasks-ask", "accept as messages", "reject"}) ||
		!strings.Contains(forms[0].Message, "cravv-connect link accept 4") {
		t.Fatalf("tasks-auto form %v %q", c, forms[0].Message)
	}
	if c := choicesOf(t, forms[1]); !slices.Equal(c, []string{"accept", "reject"}) || !strings.Contains(forms[1].Message, "SECRET-TASK wipe the disk") {
		t.Fatalf("task form %v %q", c, forms[1].Message)
	}
	decided, _ := d.got()
	want := []ipc.ReviewDecideParams{{Item: "link-4", Accept: true, Permission: "tasks-ask"}, {Item: "task-T1", Accept: true}}
	if !slices.Equal(decided, want) {
		t.Fatalf("decided %+v", decided)
	}
}

// Clients that cannot show forms: no elicitation capability (the VS Code
// extension declines, others have none), or a protocol on which a server
// may not elicit during a call. The code fallback takes over.
func TestReviewPendingWithoutForms(t *testing.T) {
	for _, tc := range []struct {
		name  string
		copts *mcp.ClientOptions
		proto string
	}{
		{"no elicitation capability", nil, claudeProto},
		{"protocol 2026-07-28", (&human{answer: answering("accept", map[string]any{"decision": "accept"})}).opts(), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newReviewDaemon(t, askItem)
			cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, tc.copts, tc.proto)
			text, isErr := callTool(t, cs, "review_pending", nil)
			decided, codes := d.got()
			if isErr || len(decided) != 0 || !slices.Equal(codes, []string{"link-3"}) || !strings.Contains(text, `call review_pending with item "link-3", decision and code`) {
				t.Fatalf("%q %v %+v %v", text, isErr, decided, codes)
			}
		})
	}
	d := newReviewDaemon(t, askItem, taskItem)
	d.codeErr = errTestNoDesktop
	cs, _ := connect(t, d.daemonFake, "claude-code")
	text, _ := callTool(t, cs, "review_pending", nil)
	if !strings.Contains(text, "cravv-connect link accept 3") || !strings.Contains(text, "cravv-connect approvals") {
		t.Fatalf("headless: %q", text)
	}
}

// What the human typed in the chat: accepting needs the code, rejecting
// does not, and a wrong code is explained.
func TestReviewPendingTypedAnswers(t *testing.T) {
	d := newReviewDaemon(t)
	cs, _ := connect(t, d.daemonFake, "claude-code")
	if text, isErr := callTool(t, cs, "review_pending", map[string]any{"item": "link-3", "decision": "accept"}); !isErr || !strings.Contains(text, "needs the 4-digit code") {
		t.Fatalf("accept without a code: %v %q", isErr, text)
	}
	if text, isErr := callTool(t, cs, "review_pending", map[string]any{"item": "link-3", "decision": "maybe"}); !isErr {
		t.Fatalf("bad decision: %q", text)
	}
	text, isErr := callTool(t, cs, "review_pending", map[string]any{"item": "link-3", "decision": "accept", "code": " 4821 "})
	if isErr || !strings.Contains(text, "link-3: accepted") {
		t.Fatalf("accept: %v %q", isErr, text)
	}
	callTool(t, cs, "review_pending", map[string]any{"item": "task-T1", "decision": "Reject"})
	decided, _ := d.got()
	want := []ipc.ReviewDecideParams{{Item: "link-3", Accept: true, Code: "4821"}, {Item: "task-T1"}}
	if !slices.Equal(decided, want) {
		t.Fatalf("decided %+v", decided)
	}
	d.decErr = errTestBadCode
	if text, isErr := callTool(t, cs, "review_pending", map[string]any{"item": "link-3", "decision": "accept", "code": "1111"}); !isErr || !strings.Contains(text, "wrong or expired") {
		t.Fatalf("bad code: %v %q", isErr, text)
	}
}

// One open form per item: a second call while the human has not answered
// does not open another.
func TestReviewPendingOneFormPerItem(t *testing.T) {
	d := newReviewDaemon(t, askItem)
	release := make(chan struct{})
	h := &human{answer: func(*mcp.ElicitParams) (*mcp.ElicitResult, error) {
		<-release
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "reject"}}, nil
	}}
	cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, h.opts(), claudeProto)
	first := make(chan string, 1)
	go func() {
		text, _ := callTool(t, cs, "review_pending", nil)
		first <- text
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.shown()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	text, _ := callTool(t, cs, "review_pending", nil)
	if !strings.Contains(text, "already open") || len(h.shown()) != 1 {
		t.Fatalf("second call: %q, forms %d", text, len(h.shown()))
	}
	close(release)
	if text := <-first; !strings.Contains(text, "link-3: rejected") {
		t.Fatalf("first call: %q", text)
	}
}
