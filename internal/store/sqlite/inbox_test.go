package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func addInbox(t *testing.T, ib store.InboxStore, msgID string, from core.MachineID, toSession string, at time.Time) int64 {
	t.Helper()
	seq, err := ib.AddItem(context.Background(), store.InboxItem{
		MsgID: msgID, From: from, FromSession: "codex@train", ToSession: toSession,
		Kind: core.KindChat, Body: json.RawMessage(`{"text":"hi"}`), ReceivedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func msgIDs(items []store.InboxItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.MsgID
	}
	return out
}

func TestInboxVisibility(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	s1 := addInbox(t, ib, "all-1", "A", "", t0)
	addInbox(t, ib, "for-p1", "A", "claude@proj", t0)
	addInbox(t, ib, "for-p2", "B", "claude@proj-2", t0)
	addInbox(t, ib, "all-2", "B", "", t0)

	cases := []struct {
		session string
		after   int64
		limit   int
		want    []string
	}{
		{"claude@proj", 0, 10, []string{"all-1", "for-p1", "all-2"}},
		{"claude@proj-2", 0, 10, []string{"all-1", "for-p2", "all-2"}},
		{"cli@other", 0, 10, []string{"all-1", "all-2"}},
		{"claude@proj", s1, 10, []string{"for-p1", "all-2"}},
		{"claude@proj", 0, 1, []string{"all-1"}},
	}
	for _, c := range cases {
		items, err := ib.ItemsFor(ctx, c.session, c.after, c.limit)
		if err != nil {
			t.Fatal(err)
		}
		if got := msgIDs(items); !equalStrings(got, c.want) {
			t.Errorf("ItemsFor(%q, after=%d, limit=%d) = %v, want %v", c.session, c.after, c.limit, got, c.want)
		}
	}
	items, _ := ib.ItemsFor(ctx, "claude@proj", 0, 1)
	it := items[0]
	if it.Seq != s1 || it.From != "A" || it.FromSession != "codex@train" || it.Kind != core.KindChat ||
		string(it.Body) != `{"text":"hi"}` || !it.ReceivedAt.Equal(t0) || it.ReadByAny {
		t.Fatalf("item fields = %+v", it)
	}
}

func TestInboxSeqIncreases(t *testing.T) {
	ib := newTestDB(t)
	a := addInbox(t, ib, "a", "A", "", t0)
	b := addInbox(t, ib, "b", "A", "", t0)
	if a <= 0 || b <= a {
		t.Fatalf("seqs %d, %d not increasing", a, b)
	}
}

func TestInboxInitialCursor(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	since := t0.Add(-core.NewSessionBacklog)

	c, err := ib.InitialCursor(ctx, since)
	if err != nil || c != 0 {
		t.Fatalf("empty inbox cursor = %d, %v; want 0", c, err)
	}

	oldRead1 := addInbox(t, ib, "old-read-1", "A", "", since.Add(-2*time.Hour))
	oldRead2 := addInbox(t, ib, "old-read-2", "A", "", since.Add(-time.Hour))
	addInbox(t, ib, "old-unread", "A", "", since.Add(-time.Minute))
	recentRead := addInbox(t, ib, "recent-read", "A", "", since.Add(time.Minute))
	if err := ib.MarkRead(ctx, []int64{oldRead1, oldRead2, recentRead}); err != nil {
		t.Fatal(err)
	}

	// max(seq) over items older than since AND read by any session.
	c, err = ib.InitialCursor(ctx, since)
	if err != nil || c != oldRead2 {
		t.Fatalf("InitialCursor = %d, %v; want %d", c, err, oldRead2)
	}
	items, _ := ib.ItemsFor(ctx, "new@proj", c, 10)
	if got := msgIDs(items); !equalStrings(got, []string{"old-unread", "recent-read"}) {
		t.Fatalf("new session sees %v", got)
	}

	// An item received exactly at `since` is not older than since.
	atSince := addInbox(t, ib, "at-since", "A", "", since)
	if err := ib.MarkRead(ctx, []int64{atSince}); err != nil {
		t.Fatal(err)
	}
	c, _ = ib.InitialCursor(ctx, since)
	if c != oldRead2 {
		t.Fatalf("item at since moved cursor to %d", c)
	}
}

func TestInboxRedirectOrphans(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	addInbox(t, ib, "for-gone-1", "A", "claude@gone", t0)
	addInbox(t, ib, "for-gone-2", "A", "claude@gone", t0)
	addInbox(t, ib, "for-other", "A", "claude@other", t0)

	n, err := ib.RedirectOrphans(ctx, "claude@gone", "(originally for claude@gone)")
	if err != nil || n != 2 {
		t.Fatalf("RedirectOrphans = %d, %v; want 2", n, err)
	}
	items, _ := ib.ItemsFor(ctx, "cli@x", 0, 10)
	if got := msgIDs(items); !equalStrings(got, []string{"for-gone-1", "for-gone-2"}) {
		t.Fatalf("after redirect, other session sees %v", got)
	}
	for _, it := range items {
		if it.ToSession != "" || it.Note != "(originally for claude@gone)" {
			t.Fatalf("redirected item = %+v", it)
		}
	}
	if n, _ := ib.RedirectOrphans(ctx, "", "x"); n != 0 {
		t.Fatalf("redirecting the machine-wide session changed %d rows", n)
	}
}

func TestInboxUnreadCountPerPeer(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	first := addInbox(t, ib, "a1", "A", "", t0)
	addInbox(t, ib, "a2", "A", "", t0)
	addInbox(t, ib, "b1", "B", "claude@proj", t0)
	addInbox(t, ib, "b2", "B", "claude@proj-2", t0)

	total, per, err := ib.UnreadCount(ctx, "claude@proj", 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || per["A"] != 2 || per["B"] != 1 || len(per) != 2 {
		t.Fatalf("UnreadCount(claude@proj, 0) = %d %v", total, per)
	}
	total, per, _ = ib.UnreadCount(ctx, "claude@proj", first)
	if total != 2 || per["A"] != 1 || per["B"] != 1 {
		t.Fatalf("UnreadCount after first = %d %v", total, per)
	}
	total, per, _ = ib.UnreadCount(ctx, "claude@proj", 1<<40)
	if total != 0 || len(per) != 0 {
		t.Fatalf("UnreadCount past end = %d %v", total, per)
	}
}

func TestInboxDeleteOlderThan(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	addInbox(t, ib, "old", "A", "", t0)
	addInbox(t, ib, "new", "A", "", t0.Add(core.InboxRetention))
	n, err := ib.PurgeInboxBefore(ctx, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v", n, err)
	}
	items, _ := ib.ItemsFor(ctx, "s", 0, 10)
	if got := msgIDs(items); !equalStrings(got, []string{"new"}) {
		t.Fatalf("remaining = %v", got)
	}
}
