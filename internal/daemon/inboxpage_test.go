package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestCheckPagesByBytes(t *testing.T) {
	ctx := context.Background()
	e := newInboxEnv(t)
	inbox, peer, name, link := e.inbox, e.peer, e.session.ID, e.link
	// Each chat wraps to 256 KiB ('<' becomes "&lt;"), so 20 of them (5 MiB)
	// cannot fit one 4 MiB page.
	const n = 20
	want := map[string]bool{}
	for range n {
		id := core.NewID()
		want[id] = true
		body, _ := json.Marshal(core.ChatBody{Text: strings.Repeat("<", core.MaxTextBytes)})
		if _, err := inbox.Deliver(ctx, store.InboxItem{MsgID: id, From: peer.MachineID, ToSession: name, LinkID: link.ID, Kind: core.KindChat, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	pages := 0
	for {
		items, err := inbox.Check(ctx, name, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		pages++
		size := 0
		for _, e := range items {
			size += len(e.Wrapped)
			if !want[e.Item.MsgID] {
				t.Fatalf("unexpected or repeated item %s", e.Item.MsgID)
			}
			delete(want, e.Item.MsgID)
		}
		if size > MaxInboxPageBytes {
			t.Fatalf("page of %d bytes is over %d", size, MaxInboxPageBytes)
		}
	}
	if pages < 2 || len(want) != 0 {
		t.Fatalf("pages = %d, missing %d items", pages, len(want))
	}
}

func TestCheckWithCancelledContextMarksNothing(t *testing.T) {
	ctx := context.Background()
	e := newInboxEnv(t)
	inbox, peer, name, link := e.inbox, e.peer, e.session.ID, e.link
	body, _ := json.Marshal(core.ChatBody{Text: "keep me"})
	inbox.Deliver(ctx, store.InboxItem{MsgID: core.NewID(), From: peer.MachineID, ToSession: name, LinkID: link.ID, Kind: core.KindChat, Body: body})

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := inbox.Check(cctx, name, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check with a cancelled ctx: err = %v", err)
	}
	if _, err := inbox.Wait(cctx, name, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait with a cancelled ctx: err = %v", err)
	}
	items, err := inbox.Check(ctx, name, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("item lost after a cancelled read: %d items, err %v", len(items), err)
	}
}

func TestWaitCancelledThenDeliverKeepsItem(t *testing.T) {
	ctx := context.Background()
	e := newInboxEnv(t)
	inbox, peer, name, link := e.inbox, e.peer, e.session.ID, e.link
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := inbox.Wait(wctx, name, 30*time.Second)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v", err)
	}
	body, _ := json.Marshal(core.ChatBody{Text: "after cancel"})
	inbox.Deliver(ctx, store.InboxItem{MsgID: core.NewID(), From: peer.MachineID, ToSession: name, LinkID: link.ID, Kind: core.KindChat, Body: body})
	if items, err := inbox.Check(ctx, name, 10); err != nil || len(items) != 1 {
		t.Fatalf("items = %d, err %v", len(items), err)
	}
}
