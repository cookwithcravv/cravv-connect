package present

import (
	"fmt"
	"sort"
	"strings"
)

// Notice builds the one-line hook message from unread counts keyed by local
// alias and the number of tasks awaiting approval. It never includes message
// content. Peers are listed alphabetically. Examples:
//
//	cravv-connect: 1 new message from gpu-box. Use check_inbox.
//	cravv-connect: 2 new messages from gpu-box, 1 task awaiting your approval. Use check_inbox.
//	cravv-connect: 3 new messages from gpu-box and 1 from laptop. Use check_inbox.
//	cravv-connect: 1 new message from a, 2 from b and 4 from c. Use check_inbox.
//	cravv-connect: 2 tasks awaiting your approval. Run cravv-connect approvals in your terminal.
//
// It returns "" when there is nothing to report.
func Notice(unread map[string]int, approvals int) string {
	aliases := make([]string, 0, len(unread))
	for a, n := range unread {
		if n > 0 {
			aliases = append(aliases, a)
		}
	}
	sort.Strings(aliases)

	var parts []string
	if len(aliases) > 0 {
		segs := make([]string, len(aliases))
		for i, a := range aliases {
			name := cleanAttr(a)
			if i == 0 {
				segs[i] = fmt.Sprintf("%d new %s from %s", unread[a], plural(unread[a], "message", "messages"), name)
			} else {
				segs[i] = fmt.Sprintf("%d from %s", unread[a], name)
			}
		}
		parts = append(parts, joinAnd(segs))
	}
	if approvals > 0 {
		parts = append(parts, fmt.Sprintf("%d %s awaiting your approval", approvals, plural(approvals, "task", "tasks")))
	}
	if len(parts) == 0 {
		return ""
	}
	tail := "Use check_inbox."
	if len(aliases) == 0 {
		tail = "Run cravv-connect approvals in your terminal."
	}
	return "cravv-connect: " + strings.Join(parts, ", ") + ". " + tail
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// joinAnd joins ["a"] -> "a", ["a","b"] -> "a and b", ["a","b","c"] -> "a, b and c".
func joinAnd(s []string) string {
	if len(s) == 1 {
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
