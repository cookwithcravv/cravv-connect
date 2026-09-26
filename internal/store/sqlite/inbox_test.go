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

// A session that already advanced its cursor past an orphaned item (for
// example a machine-wide session that read later items) must still see it
// once it is redirected: redirecting re-inserts it with a new seq.
func TestInboxRedirectOrphansReinsertsWithNewSeq(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	orphan := addInbox(t, ib, "for-gone", "A", "claude@gone", t0)
	later := addInbox(t, ib, "all-later", "B", "", t0)
	if err := ib.MarkRead(ctx, []int64{orphan, later}); err != nil {
		t.Fatal(err)
	}

	// An existing session whose cursor is already past both items.
	before, _ := ib.ItemsFor(ctx, "cli@x", later, 10)
	if len(before) != 0 {
		t.Fatalf("precondition: cursor at %d sees %v", later, msgIDs(before))
	}

	n, err := ib.RedirectOrphans(ctx, "claude@gone", "(originally for claude@gone)")
	if err != nil || n != 1 {
		t.Fatalf("RedirectOrphans = %d, %v; want 1", n, err)
	}

	items, err := ib.ItemsFor(ctx, "cli@x", later, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].MsgID != "for-gone" {
		t.Fatalf("existing session after redirect sees %v, want [for-gone]", msgIDs(items))
	}
	it := items[0]
	if it.Seq <= later || it.ToSession != "" || it.ReadByAny || it.Note != "(originally for claude@gone)" ||
		it.From != "A" || it.FromSession != "codex@train" || string(it.Body) != `{"text":"hi"}` || !it.ReceivedAt.Equal(t0) {
		t.Fatalf("redirected item = %+v", it)
	}

	// The original row is gone: nothing is visible twice.
	all, _ := ib.ItemsFor(ctx, "claude@gone", 0, 10)
	if got := msgIDs(all); !equalStrings(got, []string{"all-later", "for-gone"}) {
		t.Fatalf("all items after redirect = %v", got)
	}

	// A brand new session starts at InitialCursor, which only skips items
	// that were read; the redirected copy is unread, so it is visible.
	cur, err := ib.InitialCursor(ctx, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := ib.ItemsFor(ctx, "claude@new", cur, 10)
	if got := msgIDs(fresh); !equalStrings(got, []string{"for-gone"}) {
		t.Fatalf("new session (cursor %d) sees %v, want [for-gone]", cur, got)
	}
}

func TestHasInboxMsg(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	addInbox(t, ib, "m1", "A", "", t0)
	if ok, err := ib.HasInboxMsg(ctx, "m1"); err != nil || !ok {
		t.Fatalf("HasInboxMsg(m1) = %v, %v", ok, err)
	}
	if ok, err := ib.HasInboxMsg(ctx, "m2"); err != nil || ok {
		t.Fatalf("HasInboxMsg(m2) = %v, %v", ok, err)
	}
	// Redirecting an orphan keeps the message ID.
	addInbox(t, ib, "m3", "A", "claude@gone", t0)
	if _, err := ib.RedirectOrphans(ctx, "claude@gone", "(originally for claude@gone)"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ib.HasInboxMsg(ctx, "m3"); !ok {
		t.Fatal("redirected item lost its message ID")
	}
}

// A chat is stored once per (msg_id, to_session): a redelivery after a crash
// between the insert and the dedup mark gets the existing seq back.
func TestAddItemChatIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	first := addInbox(t, db, "M1", "PEER", "", time.UnixMilli(1000))
	again := addInbox(t, db, "M1", "PEER", "", time.UnixMilli(2000))
	if again != first {
		t.Fatalf("second insert seq %d, want existing %d", again, first)
	}
	other := addInbox(t, db, "M1", "PEER", "claude@proj", time.UnixMilli(2000))
	if other == first {
		t.Fatal("a different to_session was folded into the first item")
	}
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox WHERE msg_id = 'M1'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d, %v", n, err)
	}
	// File notices legitimately repeat a message ID (held, then done).
	for range 2 {
		if _, err := db.AddItem(ctx, store.InboxItem{MsgID: "F1", From: "PEER", Kind: core.KindFileOffer, Body: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox WHERE msg_id = 'F1'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("file notices = %d, %v", n, err)
	}
}

func TestSessionItemsAreScopedToOneSession(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	add := func(msg, to, link string) int64 {
		seq, err := db.AddItem(ctx, store.InboxItem{MsgID: msg, From: "m1", ToSession: to, LinkID: link,
			Kind: core.KindChat, Body: []byte(`{}`), ReceivedAt: t0})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	add("M0", "", "") // a v1 machine-wide item: never shown to a shared session
	s1 := add("M1", "S1", "L1")
	add("M2", "S2", "L2")
	add("M3", "S1", "L3")
	items, err := db.SessionItems(ctx, "S1", 0, 10)
	if err != nil || len(items) != 2 || items[0].MsgID != "M1" || items[1].MsgID != "M3" || items[0].LinkID != "L1" {
		t.Fatalf("SessionItems(S1) = %+v, %v", items, err)
	}
	if items, _ := db.SessionItems(ctx, "S1", s1, 10); len(items) != 1 || items[0].MsgID != "M3" {
		t.Fatalf("after cursor = %+v", items)
	}
	if items, _ := db.SessionItems(ctx, "", 0, 10); len(items) != 0 {
		t.Fatalf("empty session matched %+v", items)
	}
	total, per, err := db.SessionUnread(ctx, "S1", 0)
	if err != nil || total != 2 || per["m1"] != 2 {
		t.Fatalf("SessionUnread = %d %v %v", total, per, err)
	}
	n, err := db.DeleteSessionItems(ctx, "S1", "L3", s1)
	if err != nil || n != 1 {
		t.Fatalf("DeleteSessionItems = %d, %v", n, err)
	}
	if n, _ := db.DeleteSessionItems(ctx, "S1", "L1", s1); n != 0 {
		t.Fatalf("an item at or before the cursor was deleted")
	}
	if total, _, _ := db.SessionUnread(ctx, "S1", 0); total != 1 {
		t.Fatalf("unread after delete = %d", total)
	}
}
