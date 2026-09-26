package e2e

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// UIBrowser is a browser session on a node's web UI: it keeps the session
// cookie and submits forms with the page's CSRF token and the UI's origin.
type UIBrowser struct {
	t      *testing.T
	base   string
	client *http.Client
}

// OpenUI asks the node's daemon to start the UI (as `cravv-connect ui`
// does) and follows the launch link.
func OpenUI(t *testing.T, n *Node) *UIBrowser {
	t.Helper()
	var r ipc.UIStartResult
	Call(t, n.Conn(), ipc.MethodUIStart, nil, &r)
	u, err := url.Parse(r.URL)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatalf("launch URL %q", r.URL)
	}
	jar, _ := cookiejar.New(nil)
	b := &UIBrowser{t: t, base: "http://" + u.Host, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	code, loc, _ := b.send("GET", r.URL, nil)
	if code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("launch: %d to %q", code, loc)
	}
	return b
}

func (b *UIBrowser) send(method, target string, form url.Values) (code int, location, body string) {
	b.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = b.base + target
	}
	var rd io.Reader
	if form != nil {
		rd = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, rd)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.base)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Location"), string(raw)
}

// Get loads a page and fails the test unless it renders.
func (b *UIBrowser) Get(path string) string {
	b.t.Helper()
	code, _, body := b.send("GET", path, nil)
	if code != http.StatusOK {
		b.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	return body
}

var uiCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// Submit posts a form from page and returns the page the action redirected
// to (with its notices).
func (b *UIBrowser) Submit(page, action string, form url.Values) string {
	b.t.Helper()
	m := uiCSRF.FindStringSubmatch(b.Get(page))
	if m == nil {
		b.t.Fatalf("no form token on %s", page)
	}
	form.Set("csrf", m[1])
	code, loc, body := b.send("POST", action, form)
	if code != http.StatusSeeOther {
		b.t.Fatalf("POST %s: %d %s", action, code, body)
	}
	return b.Get(loc)
}

func wantPage(t *testing.T, page string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(page, p) {
			t.Fatalf("page lacks %q:\n%s", p, page)
		}
	}
}

// Two humans link two sessions from their web UIs alone: alice asks
// bob/trainer for a link from lead (with her password), bob accepts at a
// lower level (with his), and both daemons agree the link is active. The
// UI goes through the real IPC gates: without the password the request is
// refused.
func TestWebUIConnectAndAccept(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	ua, ub := OpenUI(t, a), OpenUI(t, b)

	wantPage(t, ua.Get("/sessions"), "<td>lead</td>", `href="/sessions?machine=bob"`)
	wantPage(t, ua.Get("/sessions?machine=bob"), "<h2>bob</h2>", `name="remote" value="trainer"`, "purpose: trainer work")

	form := url.Values{"session": {"lead"}, "machine": {"bob"}, "remote": {"trainer"}, "permission": {"tasks-ask"}, "note": {"from the web UI"}}
	wantPage(t, ua.Submit("/sessions?machine=bob", "/sessions/connect", form), "This needs your login password.")
	if len(a.AllLinks()) != 0 {
		t.Fatal("a link was requested without the password")
	}
	form.Set("password", Password)
	wantPage(t, ua.Submit("/sessions?machine=bob", "/sessions/connect", form), "asked bob/trainer for a link from lead. The other side decides.")

	in := b.WaitLink(10*time.Second, "the request arrives", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	num := strconv.FormatInt(in.Link, 10)
	wantPage(t, ub.Get("/approvals"), "<strong>alice/lead</strong> asks to link with your session <strong>trainer</strong> and asks for <strong>tasks-ask</strong>.",
		"note: from the web UI")
	accept := url.Values{"link": {num}, "permission": {"messages"}}
	wantPage(t, ub.Submit("/approvals", "/approvals/link/accept", accept), "This needs your login password.")
	accept.Set("password", Password)
	wantPage(t, ub.Submit("/approvals", "/approvals/link/accept", accept), "Accepted link "+num+": alice/lead may now use messages.",
		"Password unlocked until")

	a.WaitLink(10*time.Second, "alice sees the link accepted", func(l ipc.LinkView) bool {
		return l.State == "active" && l.RemoteSession == "trainer" && l.PermissionOut == "messages"
	})
	if l := b.Link(in.Link); l.State != "active" || l.PermissionIn != "messages" {
		t.Fatalf("bob's link %+v", l)
	}
	wantPage(t, ua.Get("/sessions"), "bob/trainer", "<td>active</td>")

	// Disconnecting from bob's page closes the link on both sides.
	wantPage(t, ub.Submit("/sessions", "/sessions/disconnect", url.Values{"link": {num}}), "Disconnected link "+num+".")
	a.WaitLink(10*time.Second, "alice sees the link closed", func(l ipc.LinkView) bool { return l.State == "closed" })
}

// The kill switch from the page, and resuming with the password.
func TestWebUIKillAndResume(t *testing.T) {
	t.Parallel()
	_, a, _ := NewPair(t)
	ua := OpenUI(t, a)
	wantPage(t, ua.Submit("/status", "/status/kill", url.Values{}), "The kill switch is on. Every link is closed.")
	if !a.Status().Killed {
		t.Fatal("not killed")
	}
	wantPage(t, ua.Get("/sessions"), "The kill switch is on. Resume on the Status page first.")
	wantPage(t, ua.Submit("/status", "/status/resume", url.Values{"password": {"wrong"}}), "Incorrect password.")
	wantPage(t, ua.Submit("/status", "/status/resume", url.Values{"password": {Password}}), "Resumed.")
	if a.Status().Killed {
		t.Fatal("still killed")
	}
}
