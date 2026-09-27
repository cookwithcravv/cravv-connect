package app

import (
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/daemon"
)

func TestInboxViewCleansPeerSession(t *testing.T) {
	v := inboxView(daemon.InboxEntry{Session: "codex@x\n\x1b[8m‮", Link: 3})
	if v.Session != "codex@x[8m" || v.Link != 3 {
		t.Fatalf("view = %+v", v)
	}
}
