package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
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

func TestInboxItemFieldsRoundTrip(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	seq := addInbox(t, ib, "m1", "A", "S1", t0)
	items, err := ib.SessionItems(ctx, "S1", 0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("SessionItems = %+v, %v", items, err)
	}
	it := items[0]
	if it.Seq != seq || it.From != "A" || it.FromSession != "codex@train" || it.ToSession != "S1" || it.Kind != core.KindChat ||
		string(it.Body) != `{"text":"hi"}` || !it.ReceivedAt.Equal(t0) {
		t.Fatalf("item fields = %+v", it)
	}
	if items, _ := ib.SessionItems(ctx, "S1", 0, 0); len(items) != 0 {
		t.Fatalf("limit 0 returned %d items", len(items))
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

func TestInboxDeleteOlderThan(t *testing.T) {
	ctx := context.Background()
	ib := newTestDB(t)
	addInbox(t, ib, "old", "A", "S1", t0)
	addInbox(t, ib, "new", "A", "S1", t0.Add(core.InboxRetention))
	n, err := ib.PurgeInboxBefore(ctx, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("DeleteOlderThan = %d, %v", n, err)
	}
	items, _ := ib.SessionItems(ctx, "S1", 0, 10)
	if got := msgIDs(items); !equalStrings(got, []string{"new"}) {
		t.Fatalf("remaining = %v", got)
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
	other := addInbox(t, db, "M1", "PEER", "S2", time.UnixMilli(2000))
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

func TestSessionUnreadGroups(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	add := func(from core.MachineID, to, link string, kind core.Kind) int64 {
		seq, err := db.AddItem(ctx, store.InboxItem{MsgID: core.NewID(), From: from, ToSession: to, LinkID: link,
			Kind: kind, Body: []byte(`{}`), ReceivedAt: t0})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	first := add("m1", "S1", "L1", core.KindChat)
	add("m2", "S1", "L2", core.KindTaskCreate)
	add("m1", "S1", "L1", core.KindChat)
	add("m1", "S2", "L9", core.KindChat) // another session
	last := add("m1", "S1", "L1", core.KindTaskUpdate)
	got, err := db.SessionUnreadGroups(ctx, "S1", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.UnreadGroup{
		{Peer: "m1", LinkID: "L1", Kind: core.KindChat, Count: 2, MaxSeq: first + 2},
		{Peer: "m2", LinkID: "L2", Kind: core.KindTaskCreate, Count: 1, MaxSeq: first + 1},
		{Peer: "m1", LinkID: "L1", Kind: core.KindTaskUpdate, Count: 1, MaxSeq: last},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups\n got %+v\nwant %+v", got, want)
	}
	if got, _ := db.SessionUnreadGroups(ctx, "S1", first+2); len(got) != 1 || got[0].Kind != core.KindTaskUpdate {
		t.Fatalf("after the cursor: %+v", got)
	}
	if got, _ := db.SessionUnreadGroups(ctx, "", 0); len(got) != 0 {
		t.Fatalf("empty session matched %+v", got)
	}
}
