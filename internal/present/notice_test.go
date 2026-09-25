package present

import (
	"strings"
	"testing"
)

func TestNotice(t *testing.T) {
	cases := []struct {
		name      string
		unread    map[string]int
		approvals int
		want      string
	}{
		{"nothing", nil, 0, ""},
		{"zero counts", map[string]int{"gpu-box": 0, "laptop": -1}, 0, ""},
		{"one message", map[string]int{"gpu-box": 1}, 0,
			"cravv-connect: 1 new message from gpu-box. Use check_inbox."},
		{"spec example", map[string]int{"gpu-box": 2}, 1,
			"cravv-connect: 2 new messages from gpu-box, 1 task awaiting your approval. Use check_inbox."},
		{"two peers sorted", map[string]int{"laptop": 1, "gpu-box": 2}, 0,
			"cravv-connect: 2 new messages from gpu-box and 1 from laptop. Use check_inbox."},
		{"three peers", map[string]int{"c": 4, "a": 1, "b": 2, "z": 0}, 3,
			"cravv-connect: 1 new message from a, 2 from b and 4 from c, 3 tasks awaiting your approval. Use check_inbox."},
		{"approvals only", map[string]int{"gpu-box": 0}, 2,
			"cravv-connect: 2 tasks awaiting your approval. Run cravv-connect approvals in your terminal."},
		{"one approval only", nil, 1,
			"cravv-connect: 1 task awaiting your approval. Run cravv-connect approvals in your terminal."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Notice(c.unread, c.approvals); got != c.want {
				t.Fatalf("Notice = %q\nwant     %q", got, c.want)
			}
		})
	}
}

func TestNoticeIsDeterministic(t *testing.T) {
	m := map[string]int{"e": 1, "d": 1, "c": 1, "b": 1, "a": 1}
	first := Notice(m, 0)
	for i := 0; i < 50; i++ {
		if got := Notice(m, 0); got != first {
			t.Fatalf("Notice changed between calls: %q vs %q", got, first)
		}
	}
}

func TestNoticeIsOneLine(t *testing.T) {
	got := Notice(map[string]int{"evil\nalias\x1b": 1}, 0)
	if strings.ContainsAny(got, "\n\r\x1b") {
		t.Fatalf("notice contains control characters: %q", got)
	}
}
