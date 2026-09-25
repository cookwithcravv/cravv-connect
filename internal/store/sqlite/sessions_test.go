package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestSessionsCRUD(t *testing.T) {
	ctx := context.Background()
	ss := newTestDB(t)
	rec := store.SessionRecord{Name: "claude@proj", Agent: "claude", ProjectDir: "/home/u/proj", Cursor: 7, LastSeen: t0, Connected: true}
	if err := ss.PutSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := ss.GetSession(ctx, "claude@proj")
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" || got.ProjectDir != "/home/u/proj" || got.Cursor != 7 || !got.LastSeen.Equal(t0) || !got.Connected {
		t.Fatalf("GetSession = %+v", got)
	}
	rec.Connected = false
	rec.LastSeen = t0.Add(time.Minute)
	if err := ss.PutSession(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := ss.SetCursor(ctx, "claude@proj", 42); err != nil {
		t.Fatal(err)
	}
	got, _ = ss.GetSession(ctx, "claude@proj")
	if got.Connected || got.Cursor != 42 || !got.LastSeen.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after update = %+v", got)
	}
	if err := ss.SetCursor(ctx, "nobody", 1); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("SetCursor missing err = %v", err)
	}
	if err := ss.PutSession(ctx, store.SessionRecord{Name: "claude@proj-2", Agent: "claude", ProjectDir: "/home/u/proj"}); err != nil {
		t.Fatal(err)
	}
	list, err := ss.ListSessions(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "claude@proj" || list[1].Name != "claude@proj-2" {
		t.Fatalf("ListSessions = %+v, %v", list, err)
	}
	if err := ss.DeleteSession(ctx, "claude@proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.GetSession(ctx, "claude@proj"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("deleted session err = %v", err)
	}
}
