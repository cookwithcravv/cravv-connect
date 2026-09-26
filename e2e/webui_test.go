package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

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

// The UI in a real browser. Headless Chrome decides for itself which
// Origin, Referer and cookies a form post carries, so this catches what the
// hand-built requests above cannot (a Referrer-Policy that makes Chrome
// send "Origin: null" refuses every action). It runs only with
// CRAVV_BROWSER_TEST=1; CRAVV_CHROME names the browser binary when it is
// not in a usual place.
func TestWebUIInARealBrowser(t *testing.T) {
	if os.Getenv("CRAVV_BROWSER_TEST") != "1" {
		t.Skip("set CRAVV_BROWSER_TEST=1 to run the headless Chrome smoke test")
	}
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome or Chromium found; set CRAVV_CHROME")
	}
	_, a, _ := NewPair(t)
	var r ipc.UIStartResult
	Call(t, a.Conn(), ipc.MethodUIStart, nil, &r)

	br := startChrome(t, chrome)
	br.navigate(r.URL)
	br.waitFor("the first page after the launch", `location.pathname === "/devices" && document.readyState === "complete"`)
	base := strings.TrimSuffix(strings.Split(r.URL, "/launch")[0], "/")
	br.navigate(base + "/status")
	br.waitFor("the Status page", `location.pathname === "/status" && document.readyState === "complete" && !!document.querySelector('form[action="/status/kill"]')`)

	// A no-password action: form.submit() skips the confirm dialog.
	br.eval(`document.querySelector('form[action="/status/kill"]').submit(), ""`)
	br.waitFor("the kill notice", `document.body.innerText.includes("The kill switch is on. Every link is closed.")`)
	if !a.Status().Killed {
		t.Fatal("the browser's kill did not reach the daemon")
	}

	// A password action, which also rotates the session cookie.
	br.eval(`(function () {
		var f = document.querySelector('form[action="/status/resume"]');
		f.querySelector('input[type=password]').value = ` + strconv.Quote(Password) + `;
		f.submit();
		return "";
	})()`)
	br.waitFor("the resume notice", `document.body.innerText.includes("Resumed.")`)
	if a.Status().Killed {
		t.Fatal("the browser's resume did not reach the daemon")
	}
	// The rotated cookie keeps working for the next page.
	br.navigate(base + "/devices")
	br.waitFor("the Devices page after the password action", `location.pathname === "/devices" && document.readyState === "complete" && document.body.innerText.includes("Paired devices")`)
}

func findChrome() string {
	if p := os.Getenv("CRAVV_CHROME"); p != "" {
		return p
	}
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// cdpBrowser drives one headless Chrome tab over the DevTools protocol: a
// tiny client that sends a command and reads until its reply, skipping
// events.
type cdpBrowser struct {
	t  *testing.T
	ws *websocket.Conn
	id int64
}

func startChrome(t *testing.T, chrome string) *cdpBrowser {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--user-data-dir="+dir, "--remote-debugging-port=0", "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	var port string
	deadline := time.Now().Add(20 * time.Second)
	for port == "" {
		if raw, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort")); err == nil {
			if lines := strings.Split(string(raw), "\n"); len(lines) >= 2 && lines[0] != "" {
				port = strings.TrimSpace(lines[0])
			}
		}
		if port == "" {
			if time.Now().After(deadline) {
				t.Fatal("Chrome did not open its DevTools port")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	var wsURL string
	for wsURL == "" {
		res, err := http.Get("http://127.0.0.1:" + port + "/json/list")
		if err == nil {
			var targets []struct{ Type, WebSocketDebuggerURL string }
			json.NewDecoder(res.Body).Decode(&targets)
			res.Body.Close()
			for _, tg := range targets {
				if tg.Type == "page" && tg.WebSocketDebuggerURL != "" {
					wsURL = tg.WebSocketDebuggerURL
					break
				}
			}
		}
		if wsURL == "" {
			if time.Now().After(deadline) {
				t.Fatal("Chrome has no page to drive")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("DevTools connection: %v", err)
	}
	ws.SetReadLimit(4 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	return &cdpBrowser{t: t, ws: ws}
}

// call sends one DevTools command and returns its result.
func (b *cdpBrowser) call(method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b.id++
	req, _ := json.Marshal(map[string]any{"id": b.id, "method": method, "params": params})
	if err := b.ws.Write(ctx, websocket.MessageText, req); err != nil {
		return nil, err
	}
	for {
		_, raw, err := b.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		var msg struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &msg) != nil || msg.ID != b.id {
			continue
		}
		if msg.Error != nil {
			return nil, errors.New(msg.Error.Message)
		}
		return msg.Result, nil
	}
}

func (b *cdpBrowser) navigate(u string) {
	b.t.Helper()
	if _, err := b.call("Page.navigate", map[string]any{"url": u}); err != nil {
		b.t.Fatalf("navigate to %s: %v", u, err)
	}
}

// eval runs a JavaScript expression in the page and returns its value as
// text ("" when it throws or the page is between documents).
func (b *cdpBrowser) eval(expr string) string {
	raw, err := b.call("Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true})
	if err != nil {
		return ""
	}
	var r struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}
	json.Unmarshal(raw, &r)
	if r.Result.Value == nil {
		return ""
	}
	return fmt.Sprint(r.Result.Value)
}

// waitFor polls cond (a JavaScript boolean expression) for 15 seconds and
// fails with the page's path and text if it never holds.
func (b *cdpBrowser) waitFor(what, cond string) {
	b.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b.eval("!!("+cond+")") == "true" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.t.Fatalf("waiting for %s; the browser shows %s:\n%s", what, b.eval("location.href"), b.eval("document.body ? document.body.innerText : ''"))
}
