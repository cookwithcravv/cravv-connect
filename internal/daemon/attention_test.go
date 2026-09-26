package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestListenWakesOnPendingItemsOnly(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	att := NewAttentionService(b.shared, b.st, b.st, b.inbox)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	trainer := shareOn(t, b, 1, "trainer", core.Visibility{Mode: core.VisibilityAllPeers})
	if _, err := att.Listen(ctx, "not-a-token", time.Millisecond); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("bad token: %v", err)
	}
	if _, err := att.Listen(ctx, trainer.ReattachToken, time.Millisecond); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the reattach token must not work as a wake token: %v", err)
	}
	if c, err := att.Listen(ctx, trainer.WakeToken, 20*time.Millisecond); err != nil || c != (Counts{}) {
		t.Fatalf("nothing pending: %+v, %v", c, err)
	}
	done := make(chan Counts, 1)
	go func() {
		c, err := att.Listen(ctx, trainer.WakeToken, 0)
		if err != nil {
			t.Error(err)
		}
		done <- c
	}()
	time.Sleep(20 * time.Millisecond) // let Listen block
	if _, err := a.links.Connect(ctx, lead.Session.ID, "bob/trainer", core.PermMessages, "hello"); err != nil {
		t.Fatal(err)
	}
	n.pump()
	select {
	case c := <-done:
		if c.Unread != 1 || c.Requests != 1 {
			t.Fatalf("counts %+v, want 1 unread notice and 1 request", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not wake")
	}
}

func TestParseAndFormatVisibility(t *testing.T) {
	ctx := context.Background()
	_, a, b := linkNet(t)
	for in, want := range map[string]core.VisibilityMode{"": core.VisibilityPrivate, "private": core.VisibilityPrivate, "all-peers": core.VisibilityAllPeers} {
		v, err := ParseVisibility(ctx, in, a.peers)
		if err != nil || v.Mode != want {
			t.Errorf("ParseVisibility(%q) = %+v, %v", in, v, err)
		}
	}
	v, err := ParseVisibility(ctx, "peers:bob, bob", a.peers)
	if err != nil || v.Mode != core.VisibilityPeers || len(v.Peers) != 1 || v.Peers[0] != b.id {
		t.Fatalf("peers:bob = %+v, %v", v, err)
	}
	if got := FormatVisibility(ctx, v, a.peers); got != "peers:bob" {
		t.Fatalf("FormatVisibility = %q", got)
	}
	for _, bad := range []string{"public", "peers:", "peers:nobody", "peers"} {
		if _, err := ParseVisibility(ctx, bad, a.peers); err == nil {
			t.Errorf("ParseVisibility(%q) succeeded", bad)
		}
	}
}
