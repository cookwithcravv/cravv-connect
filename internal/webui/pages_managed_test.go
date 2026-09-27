package webui

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

func managedDaemon(t *testing.T) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodManagedList, ipc.GateAllowWhenKilled, ipc.ManagedListResult{Sessions: []ipc.ManagedView{
		{Name: "trainer-ab12", Machine: "mac", Offer: "trainer", Folder: "/srv/train", State: "running", Link: 4, Started: true,
			LastActive: time.Date(2026, 9, 26, 11, 30, 0, 0, time.UTC)},
		{Name: "trainer-cd34", Machine: "mac", Offer: "trainer", Folder: "/srv/train", State: "idle", Link: 5},
	}})
	fd.reply(ipc.MethodOffersList, ipc.GateAllowWhenKilled, ipc.OffersListResult{Offers: []ipc.OfferView{
		{Machine: "mac", Label: "trainer", Folder: "/srv/<b>train</b>", RunMode: "shell", Permission: "tasks-auto",
			MaxConcurrent: 2, RunsPerHour: 30, RunsPerDay: 200, RunTimeoutS: 1800, IdleTimeoutS: 7200},
	}})
	fd.reply(ipc.MethodMachines, ipc.GateAllowWhenKilled, ipc.PeerListResult{Peers: []ipc.PeerView{{Alias: "mac"}}})
	fd.reply(ipc.MethodManagedClose, ipc.GateAllowWhenKilled, nil)
	fd.handle(ipc.MethodOffersSet, ipc.GateUnlock, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.OfferSetParams
		json.Unmarshal(raw, &p)
		return ipc.OfferView{Machine: p.Machine, Label: p.Label, Folder: p.Folder, RunMode: p.RunMode, Permission: p.Permission}, nil
	})
	fd.reply(ipc.MethodOffersRemove, ipc.GateUnlock, nil)
	return fd
}

func TestManagedPageShowsSessionsAndOffers(t *testing.T) {
	b := newUI(t, managedDaemon(t)).open()
	page := b.get("/managed").body
	wantContains(t, page,
		`<a href="/managed" aria-current="page">Managed</a>`,
		"<td>trainer-ab12</td><td>mac</td><td>trainer</td><td>running</td><td>4</td><td><code>/srv/train</code></td><td>2026-09-26 11:30 UTC</td>",
		`<a href="/managed?open=trainer-ab12">Open</a>`,
		`<input type="hidden" name="name" value="trainer-cd34"><button type="submit" class="danger">Close</button>`,
		"<code>/srv/&lt;b&gt;train&lt;/b&gt;</code>", "<td>shell</td><td>tasks-auto</td>",
		"2 open, 30 runs an hour, 200 a day, run 30m0s, idle 2h0m0s",
		`<option value="mac">mac</option>`, `<option value="edit-in-folder">edit-in-folder</option>`,
		"Run mode shell: the peer can run commands as your user on this machine.",
		"A shell run can do anything your user can, including talking to the local cravv-connect daemon without a token, reading ~/.cravv-connect, and editing your ~/.claude settings.")
	if strings.Contains(page, `<option value="tasks-ask">`) {
		t.Fatal("tasks-ask is offered for a managed session, which has no human to ask")
	}
	if strings.Contains(page, `href="/managed?open=trainer-cd34"`) {
		t.Fatal("a session that never ran has no conversation to open")
	}
	open := b.get("/managed?open=trainer-ab12").body
	wantContains(t, open, "<pre>cravv-connect session open trainer-ab12</pre>", "Its queue waits while you have it open",
		"This conversation was driven by mac. It opens with your normal Claude settings; review before continuing.")
	if bad := b.get("/managed?open=" + url.QueryEscape("x</pre><script>")).body; strings.Contains(bad, "session open x") {
		t.Fatal("an invalid session name is echoed")
	}
}

// Offer rules change only with the password; closing a session needs none.
func TestManagedOffersNeedThePassword(t *testing.T) {
	fd := managedDaemon(t)
	b := newUI(t, fd).open()
	form := url.Values{"machine": {"mac"}, "label": {"gpu"}, "folder": {"/srv/gpu"}, "permission": {"tasks-auto"},
		"run_mode": {"shell"}, "shell_confirm": {"shell"}, "run_timeout_m": {"90"}, "runs_per_hour": {"5"}}
	wantContains(t, b.follow("/managed", "/managed/offers/set", form).body, "This needs your login password.")
	if n := len(fd.called(ipc.MethodOffersSet)); n != 0 {
		t.Fatalf("a set without the password reached the daemon %d times", n)
	}
	form.Set("runs_per_day", "many")
	form.Set("password", "pw")
	wantContains(t, b.follow("/managed", "/managed/offers/set", form).body, "runs_per_day must be a whole number")
	form.Del("runs_per_day")
	wantContains(t, b.follow("/managed", "/managed/offers/set", form).body,
		"Offer gpu to mac: /srv/gpu (shell, tasks-auto). On mac, a chat connects to new:gpu on this machine.")
	got := fd.called(ipc.MethodOffersSet)
	want := `{"machine":"mac","label":"gpu","folder":"/srv/gpu","permission":"tasks-auto","run_mode":"shell","shell_confirm":"shell","run_timeout_s":5400,"runs_per_hour":5}`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("set calls %v\nwant %s", got, want)
	}
	remove := url.Values{"machine": {"mac"}, "label": {"trainer"}}
	wantContains(t, b.follow("/managed", "/managed/offers/remove", remove).body, "This needs your login password.")
	remove.Set("password", "pw")
	wantContains(t, b.follow("/managed", "/managed/offers/remove", remove).body, "Removed offer trainer to mac; its managed sessions closed.")
	wantContains(t, b.follow("/managed", "/managed/close", url.Values{"name": {"trainer-ab12"}}).body, "Closed trainer-ab12; its link closed too.")
	if got := fd.called(ipc.MethodManagedClose); len(got) != 1 || got[0] != `{"name":"trainer-ab12"}` {
		t.Fatalf("close calls %v", got)
	}
	// Closing works while killed (a cut-off).
	fd.setKilled(true)
	wantContains(t, b.follow("/managed", "/managed/close", url.Values{"name": {"trainer-cd34"}}).body, "Closed trainer-cd34")
	// The bad number was refused before the password was checked.
	if unlocks := len(fd.called(ipc.MethodAuthUnlock)); unlocks != 2 {
		t.Fatalf("%d unlocks, want one per password action that ran", unlocks)
	}
}
