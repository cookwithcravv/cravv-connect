package present

import (
	"strings"
	"testing"
)

func TestPendingLine(t *testing.T) {
	cases := []struct {
		name string
		in   []Pending
		want string
	}{
		{"nothing", nil, ""},
		{"zero counts", []Pending{{Link: 1, Machine: "gpu-box", Kind: PendingMessage}}, ""},
		{"spec example", []Pending{{Link: 2, Machine: "gpu-box", Kind: PendingTask, Count: 1}},
			"cravv-connect: 1 new task on link 2 from gpu-box. Call check_inbox."},
		{"plural and several links", []Pending{
			{Link: 2, Machine: "gpu-box", Kind: PendingMessage, Count: 2},
			{Link: 3, Machine: "laptop", Kind: PendingTaskUpdate, Count: 1},
		}, "cravv-connect: 2 new messages on link 2 from gpu-box, 1 new task update on link 3 from laptop. Call check_inbox."},
		{"decisions ask for review_pending", []Pending{
			{Link: 4, Machine: "gpu-box", Kind: PendingRequest, Count: 1},
			{Link: 2, Machine: "gpu-box", Kind: PendingApproval, Count: 2},
		}, "cravv-connect: 1 link request on link 4 from gpu-box, 2 tasks awaiting approval on link 2 from gpu-box. Call check_inbox, then review_pending."},
		{"capped", []Pending{
			{Link: 1, Machine: "a", Kind: PendingMessage, Count: 1},
			{Link: 2, Machine: "b", Kind: PendingFile, Count: 2},
			{Link: 3, Machine: "c", Kind: PendingLink, Count: 1},
			{Link: 4, Machine: "d", Kind: PendingMessage, Count: 3},
			{Link: 5, Machine: "e", Kind: PendingRequest, Count: 1},
		}, "cravv-connect: 1 new message on link 1 from a, 2 new files on link 2 from b, 1 link notice on link 3 from c, 4 more items. Call check_inbox, then review_pending."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PendingLine(c.in); got != c.want {
				t.Fatalf("PendingLine =\n %q\nwant\n %q", got, c.want)
			}
		})
	}
}

func TestPendingLineCleansAliasesAndHasNoEmDash(t *testing.T) {
	got := PendingLine([]Pending{{Link: 1, Machine: "gpu\u202ebox\nx", Kind: PendingMessage, Count: 1}})
	if strings.ContainsAny(got, "\u202e\n\u2014") {
		t.Fatalf("unclean line %q", got)
	}
	if !IsDecision(PendingRequest) || !IsDecision(PendingApproval) || IsDecision(PendingTaskUpdate) {
		t.Fatal("IsDecision")
	}
}
