package core

import "testing"

func TestParsePermission(t *testing.T) {
	for _, s := range []string{"messages", "tasks-ask", "tasks-auto", " tasks-ask "} {
		p, err := ParsePermission(s)
		if err != nil {
			t.Fatalf("ParsePermission(%q): %v", s, err)
		}
		if !p.Valid() {
			t.Errorf("%q not valid", p)
		}
	}
	for _, s := range []string{"", "autonomous", "chat-only", "Tasks-Auto", "tasks"} {
		if _, err := ParsePermission(s); err == nil {
			t.Errorf("ParsePermission(%q) succeeded, want error", s)
		}
	}
}

func TestPermissionOrdering(t *testing.T) {
	if !(PermMessages.Below(PermTasksAsk) && PermTasksAsk.Below(PermTasksAuto)) {
		t.Fatal("want messages < tasks-ask < tasks-auto")
	}
	if PermTasksAuto.Below(PermMessages) || PermMessages.Below(PermMessages) {
		t.Fatal("Below must be strict")
	}
	if Permission("bogus").Below(PermTasksAuto) || PermMessages.Below("bogus") {
		t.Fatal("invalid levels compare as neither below nor above")
	}
	if got := MinPermission(PermTasksAuto, PermTasksAsk); got != PermTasksAsk {
		t.Errorf("MinPermission = %q, want tasks-ask", got)
	}
	if got := MinPermission(PermMessages, PermTasksAuto); got != PermMessages {
		t.Errorf("MinPermission = %q, want messages", got)
	}
	if Permission("x").Rank() != 0 {
		t.Error("invalid permission must rank 0")
	}
}
