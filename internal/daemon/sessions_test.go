package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestSessionBaseName(t *testing.T) {
	cases := []struct{ agent, dir, want string }{
		{"claude", "/Users/p/glow-v2", "claude@glow-v2"},
		{"Claude Code", "/work/My Project", "claude-code@my-project"},
		{"codex", "/", "codex@root"},
		{"", "/tmp/x", "agent@x"},
		{"a<b>\"c", "/w/../evil\x00dir", "a-b-c@evil-dir"},
		{"claude", "/w/.hidden", "claude@hidden"},
		{strings.Repeat("x", 80), "/w/p", strings.Repeat("x", 32) + "@p"},
	}
	for _, c := range cases {
		if got := SessionBaseName(c.agent, c.dir); got != c.want {
			t.Errorf("SessionBaseName(%q, %q) = %q, want %q", c.agent, c.dir, got, c.want)
		}
	}
}

func TestSessionNameCollision(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	clock := core.NewFakeClock(d2Epoch)
	reg, inbox := d2Inbox(t, st, clock)
	peer, _ := d2Peer(t, st, "gpu-box", core.TrustAutonomous)

	first, err := reg.Register(ctx, "claude", "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reg.Register(ctx, "claude", "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
	third, err := reg.Register(ctx, "claude", "/other/proj")
	if err != nil {
		t.Fatal(err)
	}
	if first != "claude@proj" || second != "claude@proj-2" || third != "claude@proj-3" {
		t.Fatalf("names = %q %q %q", first, second, third)
	}
	if name, ok := reg.ForProjectDir(ctx, "/other/proj"); !ok || name != third {
		t.Fatalf("ForProjectDir = %q %v", name, ok)
	}
	if _, ok := reg.ForProjectDir(ctx, "/nowhere"); ok {
		t.Fatal("ForProjectDir found a session for an unused folder")
	}
	reg.Disconnect(ctx, third)
	if _, ok := reg.ForProjectDir(ctx, "/other/proj"); ok {
		t.Fatal("ForProjectDir returned a disconnected session")
	}

	env := d2Env(t, peer, core.KindChat, "codex@train", second, core.ChatBody{Text: "only for session two"})
	if err := NewChatHandler(inbox).Handle(ctx, peer, env); err != nil {
		t.Fatal(err)
	}
	got1, err := inbox.Check(ctx, first, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got1) != 0 {
		t.Fatalf("claude@proj saw %d items addressed to claude@proj-2", len(got1))
	}
	got2, err := inbox.Check(ctx, second, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || !strings.Contains(got2[0].Wrapped, "only for session two") {
		t.Fatalf("claude@proj-2 got %+v", got2)
	}
}

func TestReclaimWithinGrace(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	clock := core.NewFakeClock(d2Epoch)
	reg, inbox := d2Inbox(t, st, clock)
	var expired []string
	reg.OnExpired(func(ctx context.Context, rec store.SessionRecord) { expired = append(expired, rec.Name) })
	reg.OnExpired(func(ctx context.Context, rec store.SessionRecord) {
		if err := inbox.RedirectOrphans(ctx, rec.Name); err != nil {
			t.Errorf("redirect: %v", err)
		}
	})
	peer, _ := d2Peer(t, st, "gpu-box", core.TrustAutonomous)

	name, err := reg.Register(ctx, "claude", "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two"} {
		env := d2Env(t, peer, core.KindChat, "", "", core.ChatBody{Text: text})
		if err := NewChatHandler(inbox).Handle(ctx, peer, env); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := inbox.Check(ctx, name, 10); len(got) != 2 {
		t.Fatalf("first read got %d items", len(got))
	}
	before, err := reg.Get(ctx, name)
	if err != nil {
		t.Fatal(err)
	}

	// MCP server restart: disconnect, come back 4 minutes later.
	if err := reg.Disconnect(ctx, name); err != nil {
		t.Fatal(err)
	}
	if reg.Connected(ctx, name) {
		t.Fatal("still connected after Disconnect")
	}
	clock.Advance(4 * time.Minute)
	if err := reg.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := reg.Register(ctx, "claude", "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := reg.Get(ctx, again)
	if again != name || after.Cursor != before.Cursor || !after.Connected {
		t.Fatalf("reclaim: name %q cursor %d connected %v, want %q cursor %d", again, after.Cursor, after.Connected, name, before.Cursor)
	}
	if len(expired) != 0 {
		t.Fatalf("onExpired fired during grace: %v", expired)
	}
	if got, _ := inbox.Check(ctx, again, 10); len(got) != 0 {
		t.Fatalf("reclaimed session re-read %d items", len(got))
	}

	// A message for the session arrives, then it goes away for 6 minutes.
	env := d2Env(t, peer, core.KindChat, "", name, core.ChatBody{Text: "for the old session"})
	if err := NewChatHandler(inbox).Handle(ctx, peer, env); err != nil {
		t.Fatal(err)
	}
	if err := reg.Disconnect(ctx, name); err != nil {
		t.Fatal(err)
	}
	clock.Advance(6 * time.Minute)
	fresh, err := reg.Register(ctx, "claude", "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0] != name {
		t.Fatalf("expired = %v, want [%s]", expired, name)
	}
	rec, _ := reg.Get(ctx, fresh)
	if rec.Cursor == before.Cursor {
		t.Fatalf("new session kept the old cursor %d", rec.Cursor)
	}
	items, err := inbox.Check(ctx, fresh, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range items {
		if strings.Contains(e.Wrapped, "for the old session") {
			found = true
			if !strings.Contains(e.Wrapped, "(originally for claude@proj)") || e.Item.ToSession != "" {
				t.Fatalf("orphan not redirected with note: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("orphaned message not visible machine-wide: %+v", items)
	}
}

func TestDisconnectAllStartsGrace(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	clock := core.NewFakeClock(d2Epoch)
	reg, _ := d2Inbox(t, st, clock)
	name, _ := reg.Register(ctx, "codex", "/w/train")
	if err := reg.DisconnectAll(ctx); err != nil {
		t.Fatal(err)
	}
	if reg.Connected(ctx, name) {
		t.Fatal("session still connected after DisconnectAll")
	}
	clock.Advance(time.Minute)
	if again, _ := reg.Register(ctx, "codex", "/w/train"); again != name {
		t.Fatalf("reclaim after daemon restart got %q, want %q", again, name)
	}
}

func TestSweepKeepsConnectedSessions(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	clock := core.NewFakeClock(d2Epoch)
	reg, _ := d2Inbox(t, st, clock)
	fired := 0
	reg.OnExpired(func(context.Context, store.SessionRecord) { fired++ })
	name, _ := reg.Register(ctx, "claude", "/w/p")
	clock.Advance(time.Hour)
	if err := reg.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if !reg.Exists(ctx, name) || fired != 0 {
		t.Fatalf("connected session swept (exists=%v fired=%d)", reg.Exists(ctx, name), fired)
	}
	if _, err := reg.Get(ctx, "nobody@x"); err != core.ErrNoSession {
		t.Fatalf("Get unknown = %v, want ErrNoSession", err)
	}
}

func TestCLISessionReclaimsAfterGrace(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	clock := core.NewFakeClock(d2Epoch)
	reg, inbox := d2Inbox(t, st, clock)
	var expired []store.SessionRecord
	reg.OnExpired(func(_ context.Context, rec store.SessionRecord) { expired = append(expired, rec) })
	peer, _ := d2Peer(t, st, "gpu-box", core.TrustAutonomous)

	name, _ := reg.Register(ctx, CLIAgent, "/w/proj")
	if name != "cli@proj" {
		t.Fatalf("name = %q", name)
	}
	env := d2Env(t, peer, core.KindChat, "", "", core.ChatBody{Text: "read me once"})
	if err := NewChatHandler(inbox).Handle(ctx, peer, env); err != nil {
		t.Fatal(err)
	}
	if got, _ := inbox.Check(ctx, name, 10); len(got) != 1 {
		t.Fatal("first read failed")
	}
	before, _ := reg.Get(ctx, name)
	reg.Disconnect(ctx, name)

	clock.Advance(time.Hour) // far past core.ReclaimGrace
	if err := reg.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("cli session expired after an hour: %+v", expired)
	}
	again, _ := reg.Register(ctx, CLIAgent, "/w/proj")
	rec, _ := reg.Get(ctx, again)
	if again != name || rec.Cursor != before.Cursor {
		t.Fatalf("cli reclaim got %q cursor %d, want %q cursor %d", again, rec.Cursor, name, before.Cursor)
	}
	if got, _ := inbox.Check(ctx, again, 10); len(got) != 0 {
		t.Fatal("reclaimed cli session re-read old items")
	}

	reg.Disconnect(ctx, again)
	clock.Advance(core.InboxRetention + time.Minute)
	if err := reg.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].Agent != CLIAgent || expired[0].Name != name {
		t.Fatalf("expired = %+v", expired)
	}
}
