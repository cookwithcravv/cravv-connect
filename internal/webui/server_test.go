package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

func statusDaemon(t *testing.T) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodStatus, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.StatusResult{MachineID: "M123", DeviceName: "mac", RelayURL: "https://relay.test", RelayConnected: true, Killed: fd.isKilled()}, nil
	})
	fd.handle(ipc.MethodKill, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		fd.setKilled(true)
		return nil, nil
	})
	fd.handle(ipc.MethodResume, ipc.GateUnlock|ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		fd.setKilled(false)
		return nil, nil
	})
	return fd
}

// The launch token is swapped for an HttpOnly, SameSite=Strict cookie and a
// redirect to a URL without the token.
func TestLaunchSwapsTokenForCookie(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	launch := u.launchURL()
	if !strings.HasPrefix(launch, "http://127.0.0.1:") || !strings.Contains(launch, "/launch?token=") {
		t.Fatalf("launch URL %q", launch)
	}
	b := newBrowser(t, launch)
	r := b.do("GET", launch, nil, nil)
	if r.code != http.StatusSeeOther || r.header.Get("Location") != "/" {
		t.Fatalf("launch: %d to %q", r.code, r.header.Get("Location"))
	}
	c := b.cookie
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || len(c.Value) < 40 {
		t.Fatalf("cookie %+v", c)
	}
	if strings.Contains(r.header.Get("Set-Cookie"), "token=") {
		t.Fatal("cookie carries the token")
	}
	home := b.get("/")
	if first := DefaultRegistry().Pages()[0].Path; home.code != http.StatusSeeOther || home.header.Get("Location") != first {
		t.Fatalf("home: %d to %q", home.code, home.header.Get("Location"))
	}
	wantContains(t, b.get("/status").body, "M123", "https://relay.test (connected)")
}

// A launch token works once, and not after LaunchTokenTTL.
func TestLaunchTokenWorksOnceAndExpires(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	launch := u.launchURL()
	newBrowser(t, launch).do("GET", launch, nil, nil)
	again := newBrowser(t, launch)
	if r := again.do("GET", launch, nil, nil); r.code != http.StatusForbidden || again.cookie != nil {
		t.Fatalf("second use: %d, cookie %v", r.code, again.cookie)
	}
	if r := again.get("/status"); r.code != http.StatusUnauthorized {
		t.Fatalf("page without session: %d", r.code)
	}
	late := u.launchURL()
	u.clock.Advance(LaunchTokenTTL)
	if r := newBrowser(t, late).do("GET", late, nil, nil); r.code != http.StatusForbidden {
		t.Fatalf("expired token: %d", r.code)
	}
	bad := strings.Split(late, "token=")[0] + "token=nope"
	if r := newBrowser(t, bad).do("GET", bad, nil, nil); r.code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", r.code)
	}
}

// Only GET swaps a launch token: a HEAD (a link preview, a prefetch) or
// any other method gets 405 and leaves the token usable.
func TestOnlyGetConsumesTheLaunchToken(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	launch := u.launchURL()
	b := newBrowser(t, launch)
	for _, m := range []string{"HEAD", "POST", "PUT", "DELETE", "OPTIONS"} {
		if r := b.do(m, launch, nil, nil); r.code != http.StatusMethodNotAllowed || b.cookie != nil {
			t.Fatalf("%s: %d, cookie %v", m, r.code, b.cookie)
		}
	}
	if r := b.do("GET", launch, nil, nil); r.code != http.StatusSeeOther || b.cookie == nil {
		t.Fatalf("GET after the other methods: %d", r.code)
	}
}

// At MaxLaunchTokens live tokens, ui.start drops the oldest token only if
// it is at least LaunchTokenGrace old; otherwise it refuses with busy, so
// a flood of ui.start calls cannot knock out the link the human's browser
// is about to open.
func TestLaunchTokensAreNotEvictedWhileFresh(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	var older, newer []string
	for range MaxLaunchTokens / 2 {
		older = append(older, u.launchURL())
	}
	u.clock.Advance(LaunchTokenGrace)
	for range MaxLaunchTokens - MaxLaunchTokens/2 {
		newer = append(newer, u.launchURL())
	}
	// Each new start drops one of the older tokens.
	for range older {
		newer = append(newer, u.launchURL())
	}
	if _, err := u.l.Start(context.Background()); !errors.Is(err, ipc.ErrBusy) {
		t.Fatalf("start with only fresh tokens live: %v", err)
	}
	for _, l := range older {
		if r := newBrowser(t, l).do("GET", l, nil, nil); r.code != http.StatusForbidden {
			t.Fatalf("evicted token: %d", r.code)
		}
	}
	for _, l := range newer {
		if r := newBrowser(t, l).do("GET", l, nil, nil); r.code != http.StatusSeeOther {
			t.Fatalf("fresh token: %d", r.code)
		}
	}
	// Used tokens free their places.
	if _, err := u.l.Start(context.Background()); err != nil {
		t.Fatalf("start after the tokens were used: %v", err)
	}
}

// Only 127.0.0.1:<port> and localhost:<port> are served (DNS rebinding).
func TestHostMustBeTheUIAddress(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	port := strings.TrimPrefix(b.base, "http://127.0.0.1:")
	for host, want := range map[string]int{
		"127.0.0.1:" + port:    http.StatusOK,
		"localhost:" + port:    http.StatusOK,
		"evil.example:" + port: http.StatusForbidden,
		"127.0.0.1":            http.StatusForbidden,
		"127.0.0.1:1":          http.StatusForbidden,
		"localhost.:" + port:   http.StatusForbidden,
	} {
		r := b.do("GET", "/status", nil, func(r *http.Request) { r.Host = host })
		if r.code != want {
			t.Errorf("Host %q: %d, want %d", host, r.code, want)
		}
	}
	launch := u.launchURL()
	r := newBrowser(t, launch).do("GET", launch, nil, func(r *http.Request) { r.Host = "evil.example:" + port })
	if r.code != http.StatusForbidden {
		t.Fatalf("launch on a rebound name: %d", r.code)
	}
}

// Every response, errors and static files included, is no-store and carries
// the security headers.
func TestEveryResponseIsNoStore(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	stranger := newBrowser(t, b.base)
	for name, r := range map[string]resp{
		"page":       b.get("/status"),
		"static":     b.get("/static/app.css"),
		"no session": stranger.get("/status"),
		"bad host":   b.do("GET", "/status", nil, func(r *http.Request) { r.Host = "evil.example" }),
		"used token": stranger.get("/launch?token=x"),
		"not found":  b.get("/nope"),
	} {
		if got := r.header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", name, got)
		}
		if r.header.Get("Content-Security-Policy") == "" || r.header.Get("X-Frame-Options") != "DENY" || r.header.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: headers %v", name, r.header)
		}
	}
}

// Under Referrer-Policy: no-referrer browsers send "Origin: null" on
// same-origin form posts, which the Origin check refuses, so every action
// would fail. same-origin still keeps the UI's URLs from other sites.
func TestReferrerPolicyKeepsTheOrigin(t *testing.T) {
	b := newUI(t, statusDaemon(t)).open()
	switch p := b.get("/status").header.Get("Referrer-Policy"); p {
	case "no-referrer", "":
		t.Fatalf("Referrer-Policy %q makes browsers send Origin: null on form posts", p)
	case "same-origin":
	default:
		t.Fatalf("Referrer-Policy %q", p)
	}
}

// A state-changing request needs the cookie, the session's CSRF token and
// a matching Origin; each missing piece is refused before the daemon is
// called.
func TestActionsNeedCookieCSRFAndOrigin(t *testing.T) {
	fd := statusDaemon(t)
	u := newUI(t, fd)
	b := u.open()
	other := u.open()
	token := b.csrf("/status")
	origin := func(o string) func(*http.Request) {
		return func(r *http.Request) {
			if o != "" {
				r.Header.Set("Origin", o)
			}
		}
	}
	noCookie := newBrowser(t, b.base)
	cases := []struct {
		name string
		b    *browser
		form url.Values
		edit func(*http.Request)
		want int
	}{
		{"no cookie", noCookie, url.Values{"csrf": {token}}, origin(b.base), http.StatusUnauthorized},
		{"no csrf", b, url.Values{}, origin(b.base), http.StatusForbidden},
		{"wrong csrf", b, url.Values{"csrf": {token + "x"}}, origin(b.base), http.StatusForbidden},
		{"another session's csrf", other, url.Values{"csrf": {token}}, origin(b.base), http.StatusForbidden},
		{"no origin", b, url.Values{"csrf": {token}}, origin(""), http.StatusForbidden},
		{"foreign origin", b, url.Values{"csrf": {token}}, origin("http://evil.example"), http.StatusForbidden},
		{"other port", b, url.Values{"csrf": {token}}, origin("http://127.0.0.1:1"), http.StatusForbidden},
		{"null origin", b, url.Values{"csrf": {token}}, origin("null"), http.StatusForbidden},
	}
	for _, tc := range cases {
		if r := tc.b.do("POST", "/status/kill", tc.form, tc.edit); r.code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, r.code, tc.want)
		}
	}
	if n := len(fd.called(ipc.MethodKill)); n != 0 {
		t.Fatalf("kill called %d times by refused requests", n)
	}
	if r := b.do("GET", "/status/kill", nil, nil); r.code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on an action: %d", r.code)
	}
	r := b.do("POST", "/status/kill", url.Values{"csrf": {token}}, origin(b.base))
	if r.code != http.StatusSeeOther || r.header.Get("Location") != "/status" || len(fd.called(ipc.MethodKill)) != 1 {
		t.Fatalf("good request: %d to %q", r.code, r.header.Get("Location"))
	}
	wantContains(t, b.get("/status").body, "The kill switch is on. Every link is closed.", `action="/status/resume"`)
}

// The server stops IdleTimeout after the last request; a new ui.start
// starts a fresh one.
func TestIdleShutdownAfterThirtyMinutes(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	u.clock.Advance(IdleTimeout - time.Minute)
	u.l.sweep()
	if !u.l.Running() {
		t.Fatal("stopped before the idle timeout")
	}
	b.get("/status") // a request restarts the idle clock
	u.clock.Advance(IdleTimeout - time.Minute)
	u.l.sweep()
	if !u.l.Running() {
		t.Fatal("stopped although a request came in")
	}
	u.clock.Advance(time.Minute)
	u.l.sweep()
	if u.l.Running() {
		t.Fatal("still running 30 minutes after the last request")
	}
	if _, err := net.DialTimeout("tcp", strings.TrimPrefix(b.base, "http://"), time.Second); err == nil {
		t.Fatal("old port still accepts connections")
	}
	fresh := u.open()
	if r := fresh.get("/status"); r.code != http.StatusOK {
		t.Fatalf("after restart: %d", r.code)
	}
}

// The server stops with the daemon and refuses to start again.
func TestStopsWithTheDaemon(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	u.cancel()
	deadline := time.Now().Add(5 * time.Second)
	for u.l.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if u.l.Running() {
		t.Fatal("still running after the daemon stopped")
	}
	if _, err := u.l.Start(context.Background()); !errors.Is(err, ErrStopping) {
		t.Fatalf("start after stop: %v", err)
	}
	if _, err := net.DialTimeout("tcp", strings.TrimPrefix(b.base, "http://"), time.Second); err == nil {
		t.Fatal("port still accepts connections")
	}
}

// No password unlock outlives the request that carried the password: each
// password action unlocks a fresh daemon connection, uses it once and
// closes it. The browser session's own connection is never unlocked, so a
// second action needs the password again. Wrong passwords and the lockout
// are shown on the page.
func TestNoUnlockOutlivesTheRequest(t *testing.T) {
	fd := statusDaemon(t)
	fd.setKilled(true)
	u := newUI(t, fd)
	a := u.open()

	wantContains(t, a.follow("/status", "/status/resume", nil).body, "This needs your login password.")
	if n := len(fd.called(ipc.MethodAuthUnlock)); n != 0 {
		t.Fatalf("an empty password reached the daemon %d times", n)
	}
	wantContains(t, a.follow("/status", "/status/resume", url.Values{"password": {"nope"}}).body, "Incorrect password.")
	page := a.follow("/status", "/status/resume", url.Values{"password": {"pw"}}).body
	wantContains(t, page, "Resumed.", "Turn on the kill switch")
	if strings.Contains(page, "unlocked") {
		t.Fatal("the page shows an unlock window")
	}
	if n := u.pipes.Load(); n != 1 {
		t.Fatalf("%d daemon connections open after the password action, want only the session's", n)
	}

	fd.setKilled(true)
	wantContains(t, a.follow("/status", "/status/resume", nil).body, "This needs your login password.")
	if !fd.isKilled() {
		t.Fatal("resumed without the password right after a password action")
	}

	for range 4 {
		a.follow("/status", "/status/resume", url.Values{"password": {"nope"}})
	}
	wantContains(t, a.follow("/status", "/status/resume", url.Values{"password": {"pw"}}).body,
		"Too many wrong passwords. Password actions are locked for 15 minutes.")
}

// A cookie replayed from another client (another local process that read
// it: cookies for 127.0.0.1 are sent to every port) cannot do a password
// action without the password, and a password action by the human rotates
// the cookie and the CSRF token, so the copies stop working.
func TestReplayedCookieCannotUseThePassword(t *testing.T) {
	fd := statusDaemon(t)
	fd.setKilled(true)
	u := newUI(t, fd)
	human := u.open()
	thief := newBrowser(t, human.base)
	thief.cookie = human.cookie

	// The thief replays the cookie right after the human typed the password.
	oldCookie, oldCSRF := human.cookie.Value, human.csrf("/status")
	wantContains(t, human.follow("/status", "/status/resume", url.Values{"password": {"pw"}}).body, "Resumed.")
	fd.setKilled(true)
	if human.cookie.Value == oldCookie {
		t.Fatal("the session cookie was not rotated after a password action")
	}
	if r := thief.do("POST", "/status/resume", url.Values{"csrf": {oldCSRF}}, func(r *http.Request) { r.Header.Set("Origin", thief.base) }); r.code != http.StatusUnauthorized {
		t.Fatalf("old cookie after rotation: %d %s", r.code, r.body)
	}
	if r := thief.get("/status"); r.code != http.StatusUnauthorized {
		t.Fatalf("old cookie reads a page after rotation: %d", r.code)
	}
	// The old CSRF token does not work with the new cookie either.
	if r := human.do("POST", "/status/kill", url.Values{"csrf": {oldCSRF}}, func(r *http.Request) { r.Header.Set("Origin", human.base) }); r.code != http.StatusForbidden {
		t.Fatalf("old CSRF token after rotation: %d", r.code)
	}

	// A copy of the current cookie still needs the password.
	thief.cookie = human.cookie
	wantContains(t, thief.follow("/status", "/status/resume", nil).body, "This needs your login password.")
	if !fd.isKilled() {
		t.Fatal("a replayed cookie resumed without the password")
	}
}

// Past MaxSessions the oldest browser session ends.
func TestOldestSessionEndsPastTheCap(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	first := u.open()
	for range MaxSessions {
		u.open()
	}
	if r := first.get("/status"); r.code != http.StatusUnauthorized {
		t.Fatalf("oldest session: %d", r.code)
	}
}
