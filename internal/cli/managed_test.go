package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

func TestOffersList(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodOffersList, ipc.GateNone, ipc.OffersListResult{Offers: []ipc.OfferView{{
		Machine: "mac", Label: "trainer", Folder: "/srv/train", RunMode: "shell", Permission: "tasks-auto",
		MaxConcurrent: 2, RunsPerHour: 30, RunsPerDay: 200, RunTimeoutS: 1800, IdleTimeoutS: 7200,
	}}})
	fd.start()
	r := fd.run(nil, "offers")
	want := "" +
		"MACHINE  LABEL    FOLDER      MODE   PERMISSION  LIMITS\n" +
		"mac      trainer  /srv/train  shell  tasks-auto  2 open, 30/h, 200/day, run 30m0s, idle 2h0m0s\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("code %d\n%s\nwant\n%s", r.code, r.stdout, want)
	}
	if r := fd.run(nil, "offers", "list", "mac"); r.code != 0 || fd.params(ipc.MethodOffersList) != `{"machine":"mac"}` {
		t.Fatalf("list mac: %d %s", r.code, fd.params(ipc.MethodOffersList))
	}
}

// Setting an offer asks for the password because the daemon requires it,
// and run mode shell first needs the word shell typed at the terminal.
func TestOffersSetConfirmsShellAndAsksForThePassword(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodOffersSet, ipc.GateNone, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		if !cs.Unlocked() {
			return nil, core.ErrAuthRequired
		}
		var p ipc.OfferSetParams
		json.Unmarshal(raw, &p)
		return ipc.OfferView{Machine: p.Machine, Label: p.Label, Folder: p.Folder, RunMode: p.RunMode, Permission: p.Permission}, nil
	})
	fd.start()
	p := &fakePrompter{lines: []string{"yes"}}
	r := fd.run(p, "offers", "set", "mac", "trainer", "--folder", "train", "--permission", "tasks-auto", "--mode", "shell")
	if r.code == 0 || !strings.Contains(r.stderr, "not confirmed") || slices.Contains(fd.methods(), ipc.MethodOffersSet) {
		t.Fatalf("anything but shell must stop: code %d %q %v", r.code, r.stderr, fd.methods())
	}
	p = &fakePrompter{lines: []string{"shell"}, passwords: []string{"pw"}}
	r = fd.run(p, "offers", "set", "mac", "trainer", "--folder", "train", "--permission", "tasks-auto", "--mode", "shell",
		"--run-timeout", "2h", "--runs-per-hour", "5")
	if r.code != 0 {
		t.Fatalf("code %d %q %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, shellWarning) || !strings.Contains(r.stdout, "including talking to the local cravv-connect daemon without a token, reading ~/.cravv-connect, and editing your ~/.claude settings") ||
		!strings.Contains(r.stdout, "Offer trainer to mac: /work/glow-v2/train (shell, tasks-auto).") ||
		!strings.Contains(r.stdout, "new:trainer") {
		t.Fatalf("stdout %q", r.stdout)
	}
	if !slices.Equal(p.asked, []string{"line: Type shell to confirm", "password: " + passwordPrompt}) {
		t.Fatalf("asked %q", p.asked)
	}
	want := `{"machine":"mac","label":"trainer","folder":"/work/glow-v2/train","permission":"tasks-auto","run_mode":"shell","shell_confirm":"shell","run_timeout_s":7200,"runs_per_hour":5}`
	if got := fd.params(ipc.MethodOffersSet); got != want {
		t.Fatalf("params %s\nwant %s", got, want)
	}
	if r := fd.run(nil, "offers", "set", "mac", "x", "--permission", "messages"); r.code == 0 {
		t.Fatal("--folder is required")
	}
}

// --force sets an offer although the daemon cannot find claude now.
func TestOffersSetForce(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodOffersSet, ipc.GateUnlock, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.OfferSetParams
		json.Unmarshal(raw, &p)
		return ipc.OfferView{Machine: p.Machine, Label: p.Label, Folder: p.Folder, RunMode: "read-only", Permission: p.Permission}, nil
	})
	fd.start()
	r := fd.run(&fakePrompter{passwords: []string{"pw"}}, "offers", "set", "mac", "trainer", "--folder", "train", "--permission", "messages", "--force")
	if r.code != 0 || fd.params(ipc.MethodOffersSet) != `{"machine":"mac","label":"trainer","folder":"/work/glow-v2/train","permission":"messages","run_mode":"read-only","force":true}` {
		t.Fatalf("code %d %q params %s", r.code, r.stderr, fd.params(ipc.MethodOffersSet))
	}
}

func TestOffersRemove(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodOffersRemove, ipc.GateUnlock, func(*ipc.ConnState, json.RawMessage) (any, error) { return nil, nil })
	fd.start()
	p := &fakePrompter{passwords: []string{"pw"}}
	r := fd.run(p, "offers", "remove", "mac", "trainer")
	if r.code != 0 || r.stdout != "Removed offer trainer to mac; its managed sessions closed.\n" {
		t.Fatalf("code %d %q %q", r.code, r.stdout, r.stderr)
	}
	if got := fd.params(ipc.MethodOffersRemove); got != `{"machine":"mac","label":"trainer"}` {
		t.Fatalf("params %s", got)
	}
}

func TestSessionListAndClose(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodManagedList, ipc.GateNone, ipc.ManagedListResult{Sessions: []ipc.ManagedView{
		{Name: "trainer-ab12", Machine: "mac", Offer: "trainer", State: "running", Link: 4, Folder: "/srv/train"},
	}})
	fd.handle(ipc.MethodSessionsClose, ipc.GateAllowWhenKilled, func(_ *ipc.ConnState, p json.RawMessage) (any, error) {
		var np ipc.SessionNameParams
		json.Unmarshal(p, &np)
		kind := "managed"
		if np.Name == "lead" {
			kind = "live"
		}
		return ipc.SharedSessionView{Name: np.Name, Kind: kind, State: "closed"}, nil
	})
	fd.start()
	r := fd.run(nil, "session", "list")
	want := "" +
		"SESSION       FOR  OFFER    STATE    LINK  FOLDER\n" +
		"trainer-ab12  mac  trainer  running  4     /srv/train\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("code %d\n%s\nwant\n%s", r.code, r.stdout, want)
	}
	if r := fd.run(nil, "session", "close", "trainer-ab12"); r.code != 0 || r.stdout != "Closed trainer-ab12; its link closed too.\n" {
		t.Fatalf("close: %d %q %q", r.code, r.stdout, r.stderr)
	}
	// A chat's (live) session closes the same way, with no password.
	if r := fd.run(nil, "session", "close", "lead"); r.code != 0 || r.stdout != "Closed lead; its links closed too.\n" {
		t.Fatalf("close live: %d %q %q", r.code, r.stdout, r.stderr)
	}
	if got := fd.params(ipc.MethodSessionsClose); got != `{"name":"lead"}` {
		t.Fatalf("params %s", got)
	}
}

// session open keeps its daemon connection, and so the hold on the queue,
// for exactly as long as the conversation runs.
func TestSessionOpenHoldsTheQueueWhileOpen(t *testing.T) {
	fd := newFakeDaemon(t)
	released := make(chan struct{}, 1)
	fd.handle(ipc.MethodManagedOpen, ipc.GateNone, func(cs *ipc.ConnState, raw json.RawMessage) (any, error) {
		cs.OnClose(func() { released <- struct{}{} })
		return ipc.ManagedOpenResult{Name: "trainer-ab12", Machine: "gpu-box", Folder: "/srv/train", Command: []string{"/opt/claude", "--resume", "u-1"}}, nil
	})
	fd.start()
	var ranIn string
	var ran []string
	heldDuringRun := false
	old := runInteractive
	t.Cleanup(func() { runInteractive = old })
	runInteractive = func(_ context.Context, _ *Env, dir string, argv []string) error {
		ranIn, ran = dir, argv
		select {
		case <-released:
		case <-time.After(100 * time.Millisecond):
			heldDuringRun = true
		}
		return nil
	}
	r := fd.run(nil, "session", "open", "trainer-ab12")
	if r.code != 0 || ranIn != "/srv/train" || !slices.Equal(ran, []string{"/opt/claude", "--resume", "u-1"}) || !heldDuringRun {
		t.Fatalf("code %d %q; ran %q in %q; held %v", r.code, r.stderr, ran, ranIn, heldDuringRun)
	}
	if !strings.Contains(r.stderr, "This conversation was driven by gpu-box. It opens with your normal Claude settings; review before continuing.") {
		t.Fatalf("no warning before opening: %q", r.stderr)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the hold outlived the command")
	}
	if got := fd.params(ipc.MethodManagedOpen); got != `{"name":"trainer-ab12"}` {
		t.Fatalf("params %s", got)
	}
	runInteractive = func(context.Context, *Env, string, []string) error { return errors.New("exit status 1") }
	if r := fd.run(nil, "session", "open", "trainer-ab12"); r.code == 0 || !strings.Contains(r.stderr, "/opt/claude: exit status 1") {
		t.Fatalf("a failed agent: %d %q", r.code, r.stderr)
	}
}

func TestSessionsShowsOffers(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodSessionsList, ipc.GateNone, ipc.SessionsListResult{Machine: "gpu-box",
		Sessions: []ipc.RemoteSessionView{{Name: "trainer", Kind: "live", Agent: "claude", State: "open"}},
		Offers:   []ipc.RemoteOfferView{{Label: "gpu", Agent: "claude", MaxPermission: "tasks-auto"}},
	})
	fd.start()
	r := fd.run(nil, "sessions", "gpu-box")
	want := "" +
		"SESSION          STATE  KIND  AGENT\n" +
		"gpu-box/trainer  open   live  claude\n" +
		"\n" +
		"OFFER  MAX PERMISSION  AGENT   CONNECT TO\n" +
		"gpu    tasks-auto      claude  gpu-box/new:gpu\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("code %d\n%s\nwant\n%s", r.code, r.stdout, want)
	}
}
