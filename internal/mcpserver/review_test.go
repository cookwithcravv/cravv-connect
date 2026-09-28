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

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Stand-ins for the daemon's errors of these kinds (internal/app registers
// the real ones in the daemon process).
var (
	errTestNoDesktop = errors.New("this machine cannot show desktop notifications")
	errTestBadCode   = errors.New("wrong or expired confirmation code")
	errTestLocked    = errors.New("too many wrong confirmation codes")
)

func init() {
	ipc.RegisterErrorKind(errTestNoDesktop, ipc.KindNoDesktop)
	ipc.RegisterErrorKind(errTestBadCode, ipc.KindBadCode)
	ipc.RegisterErrorKind(errTestLocked, ipc.KindCodeLocked)
}

// Older Claude Code versions negotiate a protocol before 2026-07-28, on
// which a server elicits during a tool call (Phase 0 used this path); newer
// ones negotiate 2026-07-28, on which the tool returns input requests
// instead (SEP-2322) and the client asks the human, then calls again.
const (
	claudeProto = "2025-11-25"
	modernProto = "2026-07-28"
)

// reviewProtos are the protocols every form test runs on.
var reviewProtos = []string{claudeProto, modernProto}

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
	}
	// On protocols before 2026-07-28 the server sees these answers and falls
	// back; on 2026-07-28 the client refuses them before they reach the
	// server (see TestReviewPendingClientRefusesBadAnswers).
	oldOnly := []struct {
		name     string
		answer   func(*mcp.ElicitParams) (*mcp.ElicitResult, error)
		decided  []ipc.ReviewDecideParams
		codes    []string
		contains string
	}{
		{"accept without a decision", answering("accept", map[string]any{}), nil, []string{"link-3"}, "4-digit code"},
		{"a choice the form did not offer", answering("accept", map[string]any{"decision": "accept as tasks-auto"}), nil, []string{"link-3"}, "4-digit code"},
		{"client error", func(*mcp.ElicitParams) (*mcp.ElicitResult, error) { return nil, fmt.Errorf("no ui") }, nil, []string{"link-3"}, "4-digit code"},
	}
	for _, proto := range reviewProtos {
		all := cases
		if proto == claudeProto {
			all = append(slices.Clone(cases), oldOnly...)
		}
		for _, tc := range all {
			t.Run(proto+"/"+tc.name, func(t *testing.T) {
				d := newReviewDaemon(t, askItem)
				h := &human{answer: tc.answer}
				cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, h.opts(), proto)
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
}

// On protocol 2026-07-28 the client validates the human's answer against
// the form before sending it and reports a failed form as an error: nothing
// is decided, and the next review_pending shows the form again instead of
// waiting on the abandoned one.
func TestReviewPendingClientRefusesBadAnswers(t *testing.T) {
	bad := map[string]func(*mcp.ElicitParams) (*mcp.ElicitResult, error){
		"accept without a decision":       answering("accept", map[string]any{}),
		"a choice the form did not offer": answering("accept", map[string]any{"decision": "accept as tasks-auto"}),
		"client error":                    func(*mcp.ElicitParams) (*mcp.ElicitResult, error) { return nil, fmt.Errorf("no ui") },
	}
	for name, answer := range bad {
		t.Run(name, func(t *testing.T) {
			d := newReviewDaemon(t, askItem)
			h := &human{answer: answer}
			cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, h.opts(), modernProto)
			_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "review_pending", Arguments: map[string]any{}})
			if err == nil {
				t.Fatal("the client passed on an answer the form did not allow")
			}
			if decided, _ := d.got(); len(decided) != 0 {
				t.Fatalf("decided %+v", decided)
			}
			h.answer = answering("accept", map[string]any{"decision": "reject"})
			text, isErr := callTool(t, cs, "review_pending", nil)
			if decided, _ := d.got(); isErr || !strings.Contains(text, "link-3: rejected") || len(decided) != 1 {
				t.Fatalf("second call: %q (err %v), decided %+v", text, isErr, decided)
			}
			if forms := h.shown(); len(forms) != 2 {
				t.Fatalf("forms shown %d, want 2 (the abandoned form is asked again)", len(forms))
			}
		})
	}
}

// Review focus: a form never offers tasks-auto (the chat tier cannot grant
// it), and a task's instructions reach only the human's form, never the
// model.
func TestReviewPendingTierAndTaskText(t *testing.T) {
	for _, proto := range reviewProtos {
		t.Run(proto, func(t *testing.T) { testTierAndTaskText(t, proto) })
	}
}

func testTierAndTaskText(t *testing.T, proto string) {
	d := newReviewDaemon(t, autoItem, taskItem)
	h := &human{answer: func(p *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		if strings.Contains(p.Message, "sent a task") {
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "accept"}}, nil
		}
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "accept as tasks-ask"}}, nil
	}}
	cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, h.opts(), proto)
	text, isErr := callTool(t, cs, "review_pending", nil)
	if isErr || strings.Contains(text, "SECRET-TASK") {
		t.Fatalf("model saw %q", text)
	}
	forms := h.shown()
	if len(forms) != 2 {
		t.Fatalf("forms %d", len(forms))
	}
	// A round's forms may be shown in any order: put the link request first.
	if strings.Contains(forms[0].Message, "sent a task") {
		forms[0], forms[1] = forms[1], forms[0]
	}
	if c := choicesOf(t, forms[0]); !slices.Equal(c, []string{"accept as tasks-ask", "accept as messages", "reject"}) ||
		!strings.Contains(forms[0].Message, "cravv-connect link accept 4") || !strings.Contains(forms[0].Message, "web UI (cravv-connect ui)") {
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
		{"no elicitation capability on 2026-07-28", nil, modernProto},
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
	if !strings.Contains(text, "cravv-connect link accept 3") || !strings.Contains(text, "cravv-connect approvals") ||
		strings.Count(text, "web UI (cravv-connect ui)") != 2 {
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
	// A locked item stays locked: only the password path is offered.
	d.decErr = errTestLocked
	text, isErr = callTool(t, cs, "review_pending", map[string]any{"item": "link-3", "decision": "accept", "code": "1111"})
	if !isErr || !strings.Contains(text, "password") || !strings.Contains(text, "cravv-connect ui") || strings.Contains(text, "wait") {
		t.Fatalf("locked: %v %q", isErr, text)
	}
}

// One open form per item: a second call while the human has not answered
// does not open another.
// On protocol 2026-07-28 a parallel call gets its own round of forms, and
// the first round's answer is still applied: nothing is lost or stuck.
func TestReviewPendingParallelRoundsKeepAnswers(t *testing.T) {
	d := newReviewDaemon(t, askItem)
	release := make(chan struct{})
	var calls sync.Mutex
	n := 0
	h := &human{answer: func(*mcp.ElicitParams) (*mcp.ElicitResult, error) {
		calls.Lock()
		n++
		first := n == 1
		calls.Unlock()
		if first {
			<-release // the first form stays on screen
		}
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"decision": "reject"}}, nil
	}}
	cs, _ := connectWith(t, d.daemonFake, "claude-code", Options{}, h.opts(), modernProto)
	first := make(chan string, 1)
	go func() {
		text, _ := callTool(t, cs, "review_pending", nil)
		first <- text
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.shown()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if text, _ := callTool(t, cs, "review_pending", nil); !strings.Contains(text, "link-3: rejected") {
		t.Fatalf("parallel call: %q", text)
	}
	close(release)
	if text := <-first; !strings.Contains(text, "link-3") {
		t.Fatalf("first call lost its answer: %q", text)
	}
	if decided, _ := d.got(); len(decided) != 2 {
		t.Fatalf("decided %+v, want both answers sent (the daemon refuses the second)", decided)
	}
}

// On protocols before 2026-07-28 the form is shown within the call, and a
// parallel call waits for it instead of showing a second one.
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
