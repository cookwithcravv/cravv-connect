package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// offerPeers resolves aliases from a store (the PeerService's Resolve without its other deps).
type offerPeers struct{ st store.PeerStore }

func (p offerPeers) Resolve(ctx context.Context, addr string) (store.Peer, string, error) {
	name, session, _ := strings.Cut(addr, "/")
	peer, err := p.st.GetPeerByAlias(ctx, name)
	if errors.Is(err, core.ErrNotFound) {
		peer, err = p.st.GetPeer(ctx, core.MachineID(name))
	}
	return peer, session, err
}

// offerTree makes a fake home with a state folder and a project folder.
func offerTree(t *testing.T) (FolderRules, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	proj := filepath.Join(home, "work", "proj")
	for _, d := range []string{filepath.Join(home, ".cravv-connect"), proj} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return FolderRules{Home: home, StateDir: filepath.Join(home, ".cravv-connect")}, proj
}

type offerEvents struct{ removed []string }

func (e *offerEvents) OfferRemoved(_ context.Context, o store.Offer) {
	e.removed = append(e.removed, o.Label)
}

func newOfferSvc(t *testing.T) (*OfferService, *offerEvents, store.Peer, string) {
	t.Helper()
	st := d2Store(t)
	peer, _ := d2Peer(t, st, "mac")
	rules, proj := offerTree(t)
	svc := NewOfferService(st, offerPeers{st}, rules, core.NewFakeClock(d2Epoch), nil)
	ev := &offerEvents{}
	svc.AddObserver(ev)
	return svc, ev, peer, proj
}

func TestOfferFolderRules(t *testing.T) {
	rules, proj := offerTree(t)
	file := filepath.Join(proj, "notes.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	homeLink := filepath.Join(proj, "home-link")
	if err := os.Symlink(rules.Home, homeLink); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(rules.StateDir, "files")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, folder := range map[string]string{
		"relative":          "work/proj",
		"missing":           filepath.Join(proj, "nope"),
		"a file":            file,
		"home":              rules.Home,
		"home via symlink":  homeLink,
		"holds state dir":   filepath.Dir(rules.Home),
		"inside state dir":  inside,
		"the state dir":     rules.StateDir,
		"home with a slash": rules.Home + "/",
	} {
		if _, err := rules.Check(folder); !errors.Is(err, ErrBadFolder) {
			t.Errorf("%s (%s): err = %v, want ErrBadFolder", name, folder, err)
		}
	}
	real, err := rules.Check(proj + "/")
	if err != nil || real != proj {
		t.Fatalf("Check(proj) = %q, %v", real, err)
	}
}

func TestOfferRecheckRefusesAMovedSymlink(t *testing.T) {
	rules, proj := offerTree(t)
	a, b := filepath.Join(proj, "a"), filepath.Join(proj, "b")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(proj, "current")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	real, err := rules.Check(link)
	if err != nil || real != a {
		t.Fatalf("Check(link) = %q, %v", real, err)
	}
	o := store.Offer{Folder: link, RealFolder: real}
	if err := rules.Recheck(o); err != nil {
		t.Fatalf("unchanged folder: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	if err := rules.Recheck(o); !errors.Is(err, ErrBadFolder) || !strings.Contains(err.Error(), "set the offer again") {
		t.Fatalf("swapped symlink: err = %v", err)
	}
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	if err := rules.Recheck(o); !errors.Is(err, ErrBadFolder) {
		t.Fatalf("removed folder: err = %v", err)
	}
}

func TestOfferSetValidatesAndDefaults(t *testing.T) {
	ctx := context.Background()
	svc, _, peer, proj := newOfferSvc(t)
	in := OfferInput{Peer: "mac", Label: "trainer", Folder: proj, Permission: core.PermTasksAuto}
	if _, err := svc.Set(ctx, in, AuthChat); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("without the password: %v", err)
	}
	o, err := svc.Set(ctx, in, AuthPassword)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Offer{
		ID: o.ID, Peer: peer.MachineID, Label: "trainer", Folder: proj, RealFolder: proj, Agent: "claude",
		Permission: core.PermTasksAuto, RunMode: core.RunReadOnly, MaxConcurrent: 2, IdleTimeout: 2 * time.Hour,
		MaxTurnsPerRun: 40, RunTimeout: 30 * time.Minute, RunsPerHour: 30, RunsPerDay: 200, CreatedAt: d2Epoch, UpdatedAt: d2Epoch,
	}
	if o != want {
		t.Fatalf("Set = %+v\nwant %+v", o, want)
	}
	in.RunMode, in.RunsPerHour = core.RunEditInFolder, 3
	again, err := svc.Set(ctx, in, AuthPassword)
	if err != nil || again.ID != o.ID || again.RunMode != core.RunEditInFolder || again.RunsPerHour != 3 {
		t.Fatalf("update = %+v, %v (same label must keep its ID)", again, err)
	}
	bad := map[string]OfferInput{
		"tasks-ask":         {Peer: "mac", Label: "x", Folder: proj, Permission: core.PermTasksAsk},
		"no permission":     {Peer: "mac", Label: "x", Folder: proj},
		"bad label":         {Peer: "mac", Label: "New:X", Folder: proj, Permission: core.PermMessages},
		"long label":        {Peer: "mac", Label: strings.Repeat("a", 28), Folder: proj, Permission: core.PermMessages},
		"agent":             {Peer: "mac", Label: "x", Folder: proj, Permission: core.PermMessages, Agent: "codex"},
		"run mode":          {Peer: "mac", Label: "x", Folder: proj, Permission: core.PermMessages, RunMode: "yolo"},
		"concurrency":       {Peer: "mac", Label: "x", Folder: proj, Permission: core.PermMessages, MaxConcurrent: 21},
		"negative per hour": {Peer: "mac", Label: "x", Folder: proj, Permission: core.PermMessages, RunsPerHour: -1},
		"short idle":        {Peer: "mac", Label: "x", Folder: proj, Permission: core.PermMessages, IdleTimeout: time.Second},
		"folder":            {Peer: "mac", Label: "x", Folder: "relative", Permission: core.PermMessages},
	}
	for name, b := range bad {
		if _, err := svc.Set(ctx, b, AuthPassword); !errors.Is(err, ErrBadOffer) && !errors.Is(err, ErrBadFolder) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}
	shell := OfferInput{Peer: "mac", Label: "gpu", Folder: proj, Permission: core.PermTasksAuto, RunMode: core.RunShell}
	if _, err := svc.Set(ctx, shell, AuthPassword); !errors.Is(err, ErrShellNotConfirmed) {
		t.Fatalf("shell without confirmation: %v", err)
	}
	shell.ShellConfirm = "shell"
	if o, err := svc.Set(ctx, shell, AuthPassword); err != nil || o.RunMode != core.RunShell {
		t.Fatalf("shell confirmed: %+v, %v", o, err)
	}
	if _, err := svc.Set(ctx, OfferInput{Peer: "nobody", Label: "x", Folder: proj, Permission: core.PermMessages}, AuthPassword); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown peer: %v", err)
	}
	list, err := svc.List(ctx, "mac")
	if err != nil || len(list) != 2 || list[0].Label != "gpu" || list[1].Label != "trainer" {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

func TestOfferRemoveAndUnpair(t *testing.T) {
	ctx := context.Background()
	svc, ev, peer, proj := newOfferSvc(t)
	for _, l := range []string{"a", "b", "c"} {
		if _, err := svc.Set(ctx, OfferInput{Peer: "mac", Label: l, Folder: proj, Permission: core.PermMessages}, AuthPassword); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Remove(ctx, "mac", "a", AuthNone); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("remove without the password: %v", err)
	}
	if _, err := svc.Remove(ctx, "mac", "a", AuthPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Remove(ctx, "mac", "a", AuthPassword); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
	if err := svc.PeerCutOff(ctx, peer, CutOffPaused); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.ForPeer(ctx, peer.MachineID); len(list) != 2 {
		t.Fatalf("pausing must keep offers: %+v", list)
	}
	if err := svc.PeerCutOff(ctx, peer, CutOffUnpaired); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.ForPeer(ctx, peer.MachineID); len(list) != 0 {
		t.Fatalf("unpairing must remove offers: %+v", list)
	}
	if strings.Join(ev.removed, ",") != "a,b,c" {
		t.Fatalf("observers saw %v", ev.removed)
	}
}

// An offer is refused when the daemon cannot find claude, the only agent
// it could run, unless forced; nothing is stored then.
func TestOfferSetNeedsClaude(t *testing.T) {
	ctx := context.Background()
	svc, _, _, proj := newOfferSvc(t)
	svc.SetClaudeCheck(func() error { return ErrClaudeNotFound })
	in := OfferInput{Peer: "mac", Label: "trainer", Folder: proj, Permission: core.PermTasksAuto}
	_, err := svc.Set(ctx, in, AuthPassword)
	if !errors.Is(err, ErrClaudeNotFound) || err.Error() != "claude not found by the daemon: set CRAVV_CLAUDE in the daemon's service environment" {
		t.Fatalf("without claude: %v", err)
	}
	if list, _ := svc.List(ctx, ""); len(list) != 0 {
		t.Fatalf("stored %+v", list)
	}
	in.Force = true
	if _, err := svc.Set(ctx, in, AuthPassword); err != nil {
		t.Fatalf("forced: %v", err)
	}
	// Invalid rules are still refused first, forced or not.
	in.Label = "Bad Label"
	if _, err := svc.Set(ctx, in, AuthPassword); !errors.Is(err, ErrBadOffer) {
		t.Fatalf("bad label: %v", err)
	}
}

// ResolveClaude finds claude as FindClaude does, and says whether it did.
func TestResolveClaude(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	noenv := func(string) string { return "" }
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == EnvClaude {
				return v
			}
			return ""
		}
	}
	nowhere := func(string) (string, error) { return "", errors.New("not found") }
	onPath := func(string) (string, error) { return exe, nil }
	for _, c := range []struct {
		name   string
		getenv func(string) string
		look   func(string) (string, error)
		home   string
		want   string
		ok     bool
	}{
		{"CRAVV_CLAUDE", env(exe), nowhere, "", exe, true},
		{"CRAVV_CLAUDE missing", env(filepath.Join(dir, "gone")), onPath, "", filepath.Join(dir, "gone"), false},
		{"PATH", noenv, onPath, "", exe, true},
		{"nowhere", noenv, nowhere, t.TempDir(), "claude", false},
	} {
		got, ok := ResolveClaude(c.getenv, c.look, c.home)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %q %v, want %q %v", c.name, got, ok, c.want, c.ok)
		}
		if FindClaude(c.getenv, c.look, c.home) != got {
			t.Errorf("%s: FindClaude differs", c.name)
		}
	}
}
