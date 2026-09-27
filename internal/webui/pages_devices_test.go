package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

func devicesDaemon(t *testing.T) *fakeDaemon {
	fd := newFakeDaemon(t)
	seen := time.Date(2026, 9, 26, 11, 58, 0, 0, time.UTC)
	fd.reply(ipc.MethodMachines, ipc.GateAllowWhenKilled, ipc.PeerListResult{Peers: []ipc.PeerView{
		{Alias: "gpu-box", MachineID: "GPUMACHINEID0000000000", Online: true, LastSeen: seen, PairedAt: seen.Add(-time.Hour)},
		{Alias: "old-mac", MachineID: "OLDMAC", Paused: true},
	}})
	for _, m := range []string{ipc.MethodPeerPause, ipc.MethodPeerResume, ipc.MethodPeerUnpair} {
		fd.reply(m, ipc.GateNone, nil)
	}
	fd.reply(ipc.MethodPairStart, ipc.GateUnlock, ipc.PairStartResult{PendingID: "P1", Code: "ABCD-2345"})
	proposal := ipc.PendingPeerResult{PendingID: "P1", SuggestedName: "GPU Box <b>!", MachineID: "NEWMACHINEID00000000"}
	fd.reply(ipc.MethodPairAwait, ipc.GateUnlock, proposal)
	fd.reply(ipc.MethodJoinStart, ipc.GateUnlock, proposal)
	fd.handle(ipc.MethodPairFinalize, ipc.GateUnlock, func(_ *ipc.ConnState, p json.RawMessage) (any, error) {
		var fp ipc.PairFinalizeParams
		json.Unmarshal(p, &fp)
		return ipc.PairFinalizeResult{Alias: fp.Alias}, nil
	})
	return fd
}

func TestDevicesPageListsMachines(t *testing.T) {
	b := newUI(t, devicesDaemon(t)).open()
	page := b.get("/devices").body
	wantContains(t, page,
		"<td>gpu-box</td>", "<code>GPUMACHINEID0000</code>", "<td>online</td>", "<td>2026-09-26 11:58 UTC</td>",
		"<td>old-mac</td>", "<td>paused by you</td>", `action="/devices/resume"`, `action="/devices/pause"`,
		`data-confirm="Unpair gpu-box? Its links close, and it must pair again to reconnect."`,
		`action="/devices/pair"`, `action="/devices/join"`)
}

// Pause, resume and unpair need no password (the daemon's gates), and name
// the device by its alias.
func TestDevicesPauseResumeUnpair(t *testing.T) {
	fd := devicesDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/devices", "/devices/pause", url.Values{"alias": {"gpu-box"}}).body, "Paused gpu-box.")
	wantContains(t, b.follow("/devices", "/devices/resume", url.Values{"alias": {"old-mac"}}).body, "Resumed old-mac.")
	wantContains(t, b.follow("/devices", "/devices/unpair", url.Values{"alias": {"gpu-box"}}).body, "Unpaired gpu-box.")
	if got := fd.called(ipc.MethodPeerUnpair); len(got) != 1 || got[0] != `{"alias":"gpu-box"}` {
		t.Fatalf("unpair calls %v", got)
	}
}

var flowField = regexp.MustCompile(`name="flow" value="([^"]+)"`)

// flowID reads the pairing flow ID from a pairing step.
func flowID(t *testing.T, body string) string {
	t.Helper()
	m := flowField.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no flow field:\n%s", body)
	}
	return m[1]
}

// Pairing on the page: the password starts the pairing on a daemon
// connection of its own, the bind code shows, the wait form (submitted by
// the script, or by hand) returns the other device, and naming it with the
// password again finishes the pairing and closes that connection. The
// daemon's pending ID never reaches the page.
func TestPairNewDeviceOnThePage(t *testing.T) {
	fd := devicesDaemon(t)
	u := newUI(t, fd)
	b := u.open()
	wantContains(t, b.follow("/devices", "/devices/pair", nil).body, "This needs your login password.")

	r := b.post("/devices", "/devices/pair", url.Values{"password": {"pw"}})
	if r.code != http.StatusOK {
		t.Fatalf("pair: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, `<p class="code">ABCD-2345</p>`, "cravv-connect join ABCD-2345", `action="/devices/pair/wait" data-autosubmit`)
	flow := flowID(t, r.body)
	if strings.Contains(r.body, `value="P1"`) {
		t.Fatal("the daemon's pending ID reached the page")
	}
	if n := u.pipes.Load(); n != 2 {
		t.Fatalf("%d daemon connections open during pairing, want the session's and the pairing's", n)
	}

	r = b.post("/devices", "/devices/pair/wait", url.Values{"flow": {flow}})
	if r.code != http.StatusOK {
		t.Fatalf("wait: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, "<code>NEWMACHINEID0000</code>", `name="alias" value="gpu-box-b"`, `name="flow" value="`+flow+`"`)
	if strings.Contains(r.body, "<b>") {
		t.Fatal("the other device's suggested name reached the page unescaped")
	}
	if got := fd.called(ipc.MethodPairAwait); len(got) != 1 || got[0] != `{"pending_id":"P1"}` {
		t.Fatalf("await calls %v", got)
	}

	// Naming the device needs the password again; a wrong one keeps the
	// naming step.
	r = b.post("/devices", "/devices/pair/finish", url.Values{"flow": {flow}, "alias": {"gpu"}})
	if r.code != http.StatusOK {
		t.Fatalf("finish without the password: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, "This needs your login password.", `name="flow" value="`+flow+`"`, `name="alias" value="gpu"`)
	r = b.post("/devices", "/devices/pair/finish", url.Values{"flow": {flow}, "alias": {"gpu"}, "password": {"nope"}})
	wantContains(t, r.body, "Incorrect password.", `name="flow" value="`+flow+`"`)
	if n := len(fd.called(ipc.MethodPairFinalize)); n != 0 {
		t.Fatalf("finalize called %d times without the password", n)
	}

	page := b.follow("/devices", "/devices/pair/finish", url.Values{"flow": {flow}, "alias": {"gpu"}, "password": {"pw"}}).body
	wantContains(t, page, "Paired with gpu. Its sessions can now ask to link with yours; you decide each link.")
	if got := fd.called(ipc.MethodPairFinalize); len(got) != 1 || got[0] != `{"pending_id":"P1","alias":"gpu"}` {
		t.Fatalf("finalize calls %v", got)
	}
	if n := u.pipes.Load(); n != 1 {
		t.Fatalf("%d daemon connections open after pairing", n)
	}
	wantContains(t, b.follow("/devices", "/devices/pair/wait", url.Values{"flow": {flow}}).body, "This pairing ended.")
}

func TestJoinWithACodeOnThePage(t *testing.T) {
	fd := devicesDaemon(t)
	b := newUI(t, fd).open()
	r := b.post("/devices", "/devices/join", url.Values{"code": {" ABCD-2345 "}, "password": {"pw"}})
	if r.code != http.StatusOK {
		t.Fatalf("join: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, `action="/devices/pair/finish"`)
	flow := flowID(t, r.body)
	if got := fd.called(ipc.MethodJoinStart); len(got) != 1 || got[0] != `{"code":"ABCD-2345"}` {
		t.Fatalf("join calls %v", got)
	}
	wantContains(t, b.follow("/devices", "/devices/pair/finish", url.Values{"flow": {flow}, "alias": {"gpu"}, "password": {"pw"}}).body, "Paired with gpu.")
}

// A pairing belongs to the browser session that started it: another
// session (or a replayed old cookie) cannot use its flow ID. It ends after
// PairFlowTTL, and when its browser session ends.
func TestPairingFlowIsBoundAndExpires(t *testing.T) {
	fd := devicesDaemon(t)
	u := newUI(t, fd)
	b := u.open()
	stale := *b.cookie
	r := b.post("/devices", "/devices/pair", url.Values{"password": {"pw"}})
	flow := flowID(t, r.body)

	other := u.open()
	wantContains(t, other.follow("/devices", "/devices/pair/wait", url.Values{"flow": {flow}}).body, "This pairing ended.")
	old := newBrowser(t, b.base)
	old.cookie = &stale
	if r := old.get("/devices"); r.code != http.StatusUnauthorized {
		t.Fatalf("cookie from before the password: %d", r.code)
	}
	if n := len(fd.called(ipc.MethodPairAwait)); n != 0 {
		t.Fatalf("await called %d times by another session", n)
	}

	u.clock.Advance(PairFlowTTL)
	u.l.sweep()
	if n := u.pipes.Load(); n != 2 {
		t.Fatalf("%d daemon connections open after the pairing expired, want two sessions'", n)
	}
	wantContains(t, b.follow("/devices", "/devices/pair/wait", url.Values{"flow": {flow}}).body, "This pairing ended.")

	// Session end closes a pairing's connection.
	b.post("/devices", "/devices/pair", url.Values{"password": {"pw"}})
	if n := u.pipes.Load(); n != 3 {
		t.Fatalf("%d daemon connections open with a new pairing", n)
	}
	for range MaxSessions {
		u.open()
	}
	if r := b.get("/devices"); r.code != http.StatusUnauthorized {
		t.Fatalf("evicted session: %d", r.code)
	}
	if n := u.pipes.Load(); n != MaxSessions {
		t.Fatalf("%d daemon connections open after the session ended, want %d", n, MaxSessions)
	}
}

// The Activity page shows the audit tail newest first, with every value
// escaped and cleaned.
func TestActivityPageShowsAuditTail(t *testing.T) {
	fd := newFakeDaemon(t)
	t0 := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	fd.reply(ipc.MethodAuditRead, ipc.GateAllowWhenKilled, ipc.AuditReadResult{Events: []audit.Event{
		{TS: t0, Type: audit.EvPair, Alias: "gpu-box"},
		{TS: t0.Add(time.Minute), Type: audit.EvLinkReject, Peer: core.MachineID("PEERMACHINEID0000000"), ItemID: "L1",
			Detail: map[string]any{"reason": "<script>x</script>‮", "direction": "in"}},
	}})
	page := newUI(t, fd).open().get("/activity").body
	newer := strings.Index(page, "link_reject")
	older := strings.Index(page, "<td>pair</td>")
	if newer < 0 || older < 0 || newer > older {
		t.Fatalf("not newest first:\n%s", page)
	}
	wantContains(t, page, "<td>PEERMACHINEID000</td>", "direction=in, reason=&lt;script&gt;x&lt;/script&gt;", "2026-09-26 09:01 UTC")
	if strings.Contains(page, "<script>x") || strings.ContainsRune(page, 0x202e) {
		t.Fatal("audit detail reached the page raw")
	}
	if got := fd.called(ipc.MethodAuditRead); len(got) != 1 || got[0] != `{"limit":200}` {
		t.Fatalf("audit.read calls %v", got)
	}
}

var passwordInput = regexp.MustCompile(`<input[^>]*type="password"[^>]*>`)

// Every password field is required and asks browsers and password managers
// not to offer to save or fill it: the login password is typed each time
// and must not end up stored in the browser.
func TestPasswordFieldsAreNotSaved(t *testing.T) {
	var pages []string
	sd := statusDaemon(t)
	sd.setKilled(true)
	pages = append(pages, newUI(t, sd).open().get("/status").body)

	d := newUI(t, devicesDaemon(t)).open()
	pages = append(pages, d.get("/devices").body)
	pages = append(pages, d.post("/devices", "/devices/join", url.Values{"code": {"ABCD-2345"}, "password": {"pw"}}).body)

	a := newUI(t, approvalsDaemon(t)).open()
	pages = append(pages, a.get("/approvals").body, a.get("/sessions?machine=gpu-box").body)
	pages = append(pages, a.post("/approvals", "/approvals/tasks", url.Values{"password": {"pw"}}).body)

	n := 0
	for _, page := range pages {
		for _, in := range passwordInput.FindAllString(page, -1) {
			n++
			for _, attr := range []string{`autocomplete="off"`, `data-lpignore="true"`, ` required`, `name="password"`} {
				if !strings.Contains(in, attr) {
					t.Errorf("password field lacks %s: %s", attr, in)
				}
			}
		}
	}
	// status resume, pair, join, the naming step, link accept, show tasks,
	// connect, and one per task.
	if n < 8 {
		t.Fatalf("found only %d password fields", n)
	}
}
