package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// Large chats full of '<' (which wrapping escapes to "&lt;") arrive over
// several byte-budgeted pages, with none lost and the IPC stream intact.
func TestLargeChatsArriveInPages(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")
	sb := l.B.C
	const n = 20 // 20 x 256 KiB of wrapped text cannot fit one 4 MiB page
	want := map[string]bool{}
	for range n {
		want[sendChat(t, l.A.C, l.ANum, strings.Repeat("<", core.MaxTextBytes))] = true
	}
	pages := 0
	Eventually(t, 3*wait, "all large chats at bob", func() bool {
		items := Inbox(t, sb)
		if len(items) > 0 {
			pages++
		}
		size := 0
		for _, it := range items {
			size += len(it.Wrapped)
			if it.Kind == "chat" {
				delete(want, it.ID)
			}
		}
		if size > daemon.MaxInboxPageBytes {
			t.Fatalf("page of %d bytes", size)
		}
		return len(want) == 0
	})
	if pages < 2 {
		t.Fatalf("pages = %d, want at least 2", pages)
	}
}

// A cancelled inbox.wait does not swallow the next message.
func TestCancelledWaitKeepsMessage(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "messages")
	sb := l.B.C
	Inbox(t, sb) // read the link request notice
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sb.Call(ctx, ipc.MethodInboxWait, ipc.InboxWaitParams{TimeoutS: 50}, nil)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait err = %v", err)
	}
	// A round trip on the same connection: the server reads $/cancel before it.
	Call(t, sb, ipc.MethodStatus, nil, nil)
	id := sendChat(t, l.A.C, l.ANum, "do not lose me")
	WaitItem(t, sb, wait, "chat after a cancelled wait", isChat(id))
}
