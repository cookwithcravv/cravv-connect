package app

import (
	"testing"

	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestInboxViewCleansPeerSession(t *testing.T) {
	v := inboxView(daemon.InboxEntry{Item: store.InboxItem{FromSession: "codex@x\n\x1b[8m‮"}})
	if v.Session != "codex@x[8m" {
		t.Fatalf("session = %q", v.Session)
	}
}
