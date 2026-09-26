package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
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

// Pairing on the page: the password unlocks, the bind code shows, the wait
// form (submitted by the script, or by hand) returns the other device, and
// naming it finishes the pairing.
func TestPairNewDeviceOnThePage(t *testing.T) {
	fd := devicesDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/devices", "/devices/pair", nil).body, "This needs your login password.")

	r := b.post("/devices", "/devices/pair", url.Values{"password": {"pw"}})
	if r.code != http.StatusOK {
		t.Fatalf("pair: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, `<p class="code">ABCD-2345</p>`, "cravv-connect join ABCD-2345",
		`action="/devices/pair/wait" data-autosubmit`, `name="pending_id" value="P1"`)

	r = b.post("/devices", "/devices/pair/wait", url.Values{"pending_id": {"P1"}})
	if r.code != http.StatusOK {
		t.Fatalf("wait: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, "<code>NEWMACHINEID0000</code>", `name="alias" value="gpu-box-b"`)
	if strings.Contains(r.body, "<b>") {
		t.Fatal("the other device's suggested name reached the page unescaped")
	}
	page := b.follow("/devices", "/devices/pair/finish", url.Values{"pending_id": {"P1"}, "alias": {"gpu"}}).body
	wantContains(t, page, "Paired with gpu. Its sessions can now ask to link with yours; you decide each link.")
	if got := fd.called(ipc.MethodPairFinalize); len(got) != 1 || got[0] != `{"pending_id":"P1","alias":"gpu"}` {
		t.Fatalf("finalize calls %v", got)
	}
}

func TestJoinWithACodeOnThePage(t *testing.T) {
	fd := devicesDaemon(t)
	b := newUI(t, fd).open()
	r := b.post("/devices", "/devices/join", url.Values{"code": {" ABCD-2345 "}, "password": {"pw"}})
	if r.code != http.StatusOK {
		t.Fatalf("join: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, `action="/devices/pair/finish"`, `name="pending_id" value="P1"`)
	if got := fd.called(ipc.MethodJoinStart); len(got) != 1 || got[0] != `{"code":"ABCD-2345"}` {
		t.Fatalf("join calls %v", got)
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
