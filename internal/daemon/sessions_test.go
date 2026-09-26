package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
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

func TestAttachmentNameCollision(t *testing.T) {
	ctx := context.Background()
	reg := NewSessionRegistry(d2Store(t), core.NewFakeClock(d2Epoch))
	first, err := reg.Register(ctx, "claude", "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := reg.Register(ctx, "claude", "/work/proj")
	third, _ := reg.Register(ctx, "claude", "/other/proj")
	if first != "claude@proj" || second != "claude@proj-2" || third != "claude@proj-3" {
		t.Fatalf("names = %q %q %q", first, second, third)
	}
}

// An MCP server restart gets its attachment name back within the grace.
func TestReclaimWithinGrace(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(d2Epoch)
	reg := NewSessionRegistry(d2Store(t), clock)
	name, err := reg.Register(ctx, "claude", "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil || again != name || !reg.Connected(ctx, again) {
		t.Fatalf("reclaim: %q %v, want %q", again, err, name)
	}
	if err := reg.Disconnect(ctx, name); err != nil {
		t.Fatal(err)
	}
	clock.Advance(core.ReclaimGrace + time.Second)
	if err := reg.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if reg.Exists(ctx, name) {
		t.Fatal("expired attachment not swept")
	}
}

func TestDisconnectAllStartsGrace(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(d2Epoch)
	reg := NewSessionRegistry(d2Store(t), clock)
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
	clock := core.NewFakeClock(d2Epoch)
	reg := NewSessionRegistry(d2Store(t), clock)
	name, _ := reg.Register(ctx, "claude", "/w/p")
	clock.Advance(time.Hour)
	if err := reg.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if !reg.Exists(ctx, name) {
		t.Fatal("connected session swept")
	}
	if _, err := reg.Get(ctx, "nobody@x"); err != core.ErrNoSession {
		t.Fatalf("Get unknown = %v, want ErrNoSession", err)
	}
}
