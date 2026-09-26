package core

import (
	"fmt"
	"strings"
)

// Permission is what the other end of a link may do to this side's session
// (a link's permission_in). Levels are ordered: messages < tasks-ask < tasks-auto.
type Permission string

const (
	// PermMessages allows chat and files within the link.
	PermMessages Permission = "messages"
	// PermTasksAsk adds tasks that each need a human decision on this side.
	PermTasksAsk Permission = "tasks-ask"
	// PermTasksAuto adds tasks the agent may carry out without asking.
	PermTasksAuto Permission = "tasks-auto"
)

// permissionRank orders the levels; 0 means invalid.
var permissionRank = map[Permission]int{PermMessages: 1, PermTasksAsk: 2, PermTasksAuto: 3}

// ParsePermission parses "messages", "tasks-ask" or "tasks-auto".
func ParsePermission(s string) (Permission, error) {
	p := Permission(strings.TrimSpace(s))
	if !p.Valid() {
		return "", fmt.Errorf("invalid permission %q (use messages, tasks-ask, or tasks-auto)", s)
	}
	return p, nil
}

// Valid reports whether p is one of the three levels.
func (p Permission) Valid() bool { return permissionRank[p] > 0 }

// Rank is 1 for messages, 2 for tasks-ask, 3 for tasks-auto and 0 for anything else.
func (p Permission) Rank() int { return permissionRank[p] }

// Below reports whether p is a lower level than q (both valid).
func (p Permission) Below(q Permission) bool { return p.Valid() && q.Valid() && p.Rank() < q.Rank() }

// MinPermission returns the lower of two valid levels.
func MinPermission(a, b Permission) Permission {
	if b.Below(a) {
		return b
	}
	return a
}
