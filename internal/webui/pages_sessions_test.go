package webui

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
)

// hostile is peer-chosen text that tries to break out of the page, hide
// itself and reorder what the human reads.
const hostile = "</pre><script>alert(1)</script><img src=x onerror=alert(2)>‮evil​"

func wrapped(kind, body string) string {
	return present.Wrap(present.Item{Alias: "gpu-box", Session: "trainer", ID: "X1", Kind: kind, Body: body})
}

var sampleLinkViews = []ipc.LinkView{
	{Link: 1, Machine: "gpu-box", Session: "lead", RemoteSession: "trainer", Direction: "out", State: "active",
		PermissionIn: "tasks-auto", PermissionOut: "tasks-ask", Wrapped: wrapped("link", "purpose: "+hostile)},
	{Link: 2, Machine: "mac", Session: "lead", RemoteSession: "helper", Direction: "in", State: "pending", Proposed: "tasks-ask",
		Wrapped: wrapped("link", "note: please link "+hostile)},
	{Link: 3, Machine: "gpu-box", Session: "lead", RemoteSession: "old", Direction: "out", State: "closed", Reason: "presence_timeout", PermissionIn: "messages"},
	{Link: 4, Machine: "gpu-box", Session: "side", RemoteSession: "eval", Direction: "in", State: "active", PermissionIn: "messages", RemoteAway: true},
	{Link: 6, Machine: "mac", Session: "side", RemoteSession: "far", Direction: "out", State: "active", PermissionIn: "messages", RemoteAway: true, Unreachable: true},
}

func sessionsDaemon(t *testing.T) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodSessionsLocal, ipc.GateAllowWhenKilled, ipc.LocalSessionsResult{Sessions: []ipc.SharedSessionView{
		{Name: "lead", Purpose: "coordinate the run", Visibility: "all-peers", State: "open", Kind: "live", Agent: "claude"},
		{Name: "side", Visibility: "peers:gpu-box", State: "open", Kind: "live", Agent: "codex"},
		{Name: "nap", Visibility: "private", State: "away", Kind: "live", Agent: "claude"},
	}})
	fd.reply(ipc.MethodLinks, ipc.GateNone, ipc.LinksResult{Links: sampleLinkViews})
	fd.reply(ipc.MethodMachines, ipc.GateAllowWhenKilled, ipc.PeerListResult{Peers: []ipc.PeerView{{Alias: "gpu-box"}, {Alias: "mac"}}})
	fd.reply(ipc.MethodSessionsList, ipc.GateNone, ipc.SessionsListResult{Machine: "gpu-box", Sessions: []ipc.RemoteSessionView{
		{Name: "trainer", Kind: "live", Agent: "claude", State: "open", Wrapped: wrapped("session", "purpose: "+hostile)},
	}})
	fd.handle(ipc.MethodLinkConnectAs, ipc.GateUnlock, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.LinkView{Link: 5, State: "pending"}, nil
	})
	fd.reply(ipc.MethodLinkDisconnect, ipc.GateNone, nil)
	fd.handle(ipc.MethodLinkRestrict, ipc.GateNone, func(_ *ipc.ConnState, p json.RawMessage) (any, error) {
		var lp ipc.LinkPermissionParams
		json.Unmarshal(p, &lp)
		return ipc.LinkView{Link: lp.Link, PermissionIn: lp.Permission}, nil
	})
	return fd
}

func TestSessionsPageShowsLocalSessionsAndLinks(t *testing.T) {
	b := newUI(t, sessionsDaemon(t)).open()
	page := b.get("/sessions").body
	wantContains(t, page,
		"<td>lead</td><td>coordinate the run</td><td>all-peers</td><td>open</td><td>claude</td>",
		"<td>nap</td><td>-</td><td>private</td><td>away</td>",
		"<td>1</td><td>lead</td><td>gpu-box/trainer",
		"<td>active</td><td>tasks-auto</td><td>tasks-ask</td>",
		`<option value="messages">messages</option><option value="tasks-ask">tasks-ask</option></select><button type="submit" class="secondary">Restrict</button>`,
		"<td>closed: presence_timeout</td>", "<td>active (peer away)</td>", "<td>away (peer machine not answering)</td>",
		"Link requests waiting for you: 1.",
		`href="/sessions?machine=gpu-box"`, `href="/sessions?machine=mac"`)
	if strings.Contains(page, `name="link" value="3"`) {
		t.Fatal("a closed link offers actions")
	}
	if strings.Contains(page, "mac/helper") {
		t.Fatal("a pending request is listed with the links; it belongs on the Approvals page")
	}
}

// Peer-chosen text is escaped by html/template and cleaned of hiding and
// reordering characters before it reaches the page.
func TestPeerStringsAreEscapedAndCleaned(t *testing.T) {
	b := newUI(t, sessionsDaemon(t)).open()
	for _, page := range []string{b.get("/sessions?machine=gpu-box").body, b.get("/approvals").body} {
		if strings.Contains(page, "<script>alert") || strings.Contains(page, "<img src=x") || strings.Contains(page, "</pre><script") {
			t.Fatalf("peer markup reached the page:\n%s", page)
		}
		if strings.ContainsRune(page, 0x202E) || strings.ContainsRune(page, 0x200B) {
			t.Fatal("hiding characters reached the page")
		}
		wantContains(t, page, "&lt;/pre&gt;&lt;script&gt;alert(1)&lt;/script&gt;&lt;img src=x onerror=alert(2)&gt;evil")
	}
}

// Asking for a link on a session's behalf needs the password and sends the
// chosen session, target, permission and note.
func TestConnectFromTheSessionsPage(t *testing.T) {
	fd := sessionsDaemon(t)
	b := newUI(t, fd).open()
	page := b.get("/sessions?machine=gpu-box").body
	wantContains(t, page, "<h2>gpu-box</h2>", `name="remote" value="trainer"`,
		`<option value="lead">lead</option><option value="side">side</option></select>`)
	if strings.Contains(page, `<option value="nap">`) {
		t.Fatal("an away session is offered for connecting")
	}
	form := url.Values{"session": {"lead"}, "machine": {"gpu-box"}, "remote": {"trainer"}, "permission": {"tasks-ask"}, "note": {"hi"}}
	wantContains(t, b.follow("/sessions", "/sessions/connect", form).body, "This needs your login password.")
	form.Set("password", "pw")
	wantContains(t, b.follow("/sessions", "/sessions/connect", form).body,
		"Link 5: asked gpu-box/trainer for a link from lead. The other side decides.")
	// The daemon's gate refused the first try before the handler ran.
	if got := fd.called(ipc.MethodLinkConnectAs); len(got) != 1 || got[0] != `{"session":"lead","target":"gpu-box/trainer","permission":"tasks-ask","note":"hi"}` {
		t.Fatalf("connect calls %v", got)
	}
}

// Disconnect and restrict need no password.
func TestDisconnectAndRestrict(t *testing.T) {
	fd := sessionsDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/sessions", "/sessions/restrict", url.Values{"link": {"1"}, "permission": {"messages"}}).body, "Link 1 now allows messages.")
	wantContains(t, b.follow("/sessions", "/sessions/disconnect", url.Values{"link": {"1"}}).body, "Disconnected link 1.")
	wantContains(t, b.follow("/sessions", "/sessions/disconnect", url.Values{"link": {"x"}}).body, "bad request: link must be a link number")
	if got := fd.called(ipc.MethodLinkDisconnect); len(got) != 1 || got[0] != `{"link":1}` {
		t.Fatalf("disconnect calls %v", got)
	}
}

// Every page still renders while the kill switch is on, with the daemon's
// refusal shown.
func TestSessionsPageWhileKilled(t *testing.T) {
	fd := sessionsDaemon(t)
	fd.setKilled(true)
	page := newUI(t, fd).open().get("/sessions").body
	wantContains(t, page, "<td>lead</td>", "The kill switch is on. Resume on the Status page first.")
	if strings.Contains(page, "gpu-box/trainer") {
		t.Fatal("links shown although the daemon refused them")
	}
}
