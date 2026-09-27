package e2e

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// v2 spec 7.2 over IPC: the chat's connection lists its pending decisions,
// a confirmation code shown only on the desktop accepts a request at most
// at tasks-ask, a wrong code does not, and the code never travels over IPC.
func TestReviewWithConfirmationCode(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	lead := a.Share("claude", "lead", "private")
	trainer := b.Share("claude", "trainer", "all-peers")
	out := Connect(t, lead, "bob/trainer", "tasks-auto", "REVIEW-NOTE train it")
	in := b.WaitLink(wait, "request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })

	var raw json.RawMessage
	Call(t, trainer.C, ipc.MethodReviewList, nil, &raw)
	var list ipc.ReviewListResult
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	item := list.Items[0]
	if len(list.Items) != 1 || item.Kind != "link" || item.Link != in.Link || item.Machine != "alice" || item.Session != "lead" ||
		item.Permission != "tasks-auto" || !strings.Contains(item.Wrapped, "REVIEW-NOTE train it") {
		t.Fatalf("review list %s", raw)
	}
	other := b.Share("codex", "other", "private")
	var none ipc.ReviewListResult
	Call(t, other.C, ipc.MethodReviewList, nil, &none)
	if len(none.Items) != 0 {
		t.Fatalf("another session sees %+v", none.Items)
	}
	wantKind(t, TryCall(b.Conn(), ipc.MethodReviewList, nil, nil), ipc.KindNotShared)

	// An answer from a form cannot grant tasks-auto.
	wantKind(t, TryCall(trainer.C, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: item.Item, Accept: true}, nil), ipc.KindAuthRequired)

	var shown json.RawMessage
	Call(t, trainer.C, ipc.MethodReviewCode, ipc.ReviewItemParams{Item: item.Item}, &shown)
	code := b.Desktop.Code()
	if len(code) != 4 {
		t.Fatalf("no code on the desktop: %q", code)
	}
	if _, text := b.Desktop.Last(); !strings.HasPrefix(text, "Code "+code+". Link request asking tasks-auto.") || !strings.HasSuffix(text, "From alice/lead.") {
		t.Fatalf("notification %q", text)
	}
	if strings.Contains(string(raw)+string(shown), code) {
		t.Fatalf("the code travelled over IPC: %s %s", raw, shown)
	}
	wrong := "0000"
	if code == wrong {
		wrong = "1111"
	}
	wantKind(t, TryCall(trainer.C, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: item.Item, Accept: true, Code: wrong}, nil), ipc.KindBadCode)
	wantKind(t, TryCall(other.C, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: item.Item, Accept: true, Code: code}, nil), ipc.KindNotFound)
	var res ipc.ReviewDecideResult
	Call(t, trainer.C, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: item.Item, Accept: true, Code: code}, &res)
	if res.Outcome != "accepted" || res.Permission != "tasks-ask" || res.Link != in.Link {
		t.Fatalf("decide %+v", res)
	}
	a.WaitLink(wait, "accepted at tasks-ask", func(l ipc.LinkView) bool {
		return l.Link == out.Link && l.State == "active" && l.PermissionOut == "tasks-ask"
	})
	wantKind(t, TryCall(trainer.C, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: item.Item, Accept: true, Code: code}, nil), ipc.KindNotFound)
}

// A headless machine has no desktop: review.code says so, and the human
// uses the CLI (the password path) instead.
func TestReviewCodeOnAHeadlessMachine(t *testing.T) {
	t.Parallel()
	r := NewRelay(t)
	a := NewNode(t, r, "alice", NodeOptions{AdminToken: AdminToken})
	b := NewNode(t, r, "bob", NodeOptions{Headless: true})
	Pair(t, a, b)
	lead := a.Share("claude", "lead", "private")
	trainer := b.Share("claude", "trainer", "all-peers")
	Connect(t, lead, "bob/trainer", "messages", "")
	in := b.WaitLink(wait, "request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	wantKind(t, TryCall(trainer.C, ipc.MethodReviewCode, ipc.ReviewItemParams{Item: "link-" + strconv.FormatInt(in.Link, 10)}, nil), ipc.KindNoDesktop)
	if b.Desktop.Code() != "" {
		t.Fatal("a headless node showed a code")
	}
	if got := b.Decide(in.Link, true, ""); got.State != "active" {
		t.Fatalf("password path %+v", got)
	}
}
