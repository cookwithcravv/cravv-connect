package api

import (
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// Decisions in chat act only for the session shared on the connection.
func TestReviewNeedsTheSharedSession(t *testing.T) {
	h := newHarness(t)
	lw := h.w.lw
	c := h.session(t)
	for _, m := range []string{ipc.MethodReviewList, ipc.MethodReviewDecide, ipc.MethodReviewCode} {
		if err := c.Call(bg, m, map[string]any{"item": "link-3"}, nil); !errors.Is(err, core.ErrNotShared) {
			t.Fatalf("%s before sharing: %v", m, err)
		}
	}
	if err := c.Call(bg, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "trainer"}, nil); err != nil {
		t.Fatal(err)
	}
	var list ipc.ReviewListResult
	if err := c.Call(bg, ipc.MethodReviewList, nil, &list); err != nil || len(list.Items) != 1 || lw.last() != "review.list S1" {
		t.Fatalf("list %+v, %v, %q", list, err, lw.last())
	}
	var res ipc.ReviewDecideResult
	if err := c.Call(bg, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: "link-3", Accept: true, Code: "4821"}, &res); err != nil || res.Outcome != "accepted" {
		t.Fatalf("decide %+v, %v", res, err)
	}
	if got := lw.last(); got != "review.decide S1 link-3 true  4821" {
		t.Fatalf("decide call %q", got)
	}
	if err := c.Call(bg, ipc.MethodReviewDecide, ipc.ReviewDecideParams{}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("decide without an item: %v", err)
	}
	var out map[string]any
	if err := c.Call(bg, ipc.MethodReviewCode, ipc.ReviewItemParams{Item: "link-3"}, &out); err != nil || len(out) != 0 {
		t.Fatalf("code result %v, %v: it must carry nothing", out, err)
	}
	if got := lw.last(); got != "review.code S1 link-3" {
		t.Fatalf("code call %q", got)
	}
}
