package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// fakeDaemon is a real ipc.Server with scripted handlers. UI sessions reach
// it over in-process pipes, so its gates (password, kill switch) are real.
// auth.unlock accepts "pw"; five wrong passwords lock it.
type fakeDaemon struct {
	t      *testing.T
	clock  *core.FakeClock
	srv    *ipc.Server
	mu     sync.Mutex
	calls  []string
	killed bool
	fails  int
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	fd := &fakeDaemon{t: t, clock: core.NewFakeClock(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))}
	fd.srv = ipc.NewServer(ipc.Options{Clock: fd.clock, Killed: fd.isKilled})
	fd.handle(ipc.MethodAuthUnlock, ipc.GateAllowWhenKilled, func(cs *ipc.ConnState, p json.RawMessage) (any, error) {
		var up ipc.UnlockParams
		json.Unmarshal(p, &up)
		fd.mu.Lock()
		defer fd.mu.Unlock()
		if fd.fails >= core.LockoutFailures {
			return nil, core.ErrLocked
		}
		if up.Password != "pw" {
			fd.fails++
			return nil, core.ErrBadPassword
		}
		return ipc.UnlockResult{ExpiresAt: cs.Unlock(core.UnlockTTL)}, nil
	})
	return fd
}

// handle registers method; every call is recorded as "method params".
func (fd *fakeDaemon) handle(method string, gate ipc.Gate, fn func(cs *ipc.ConnState, p json.RawMessage) (any, error)) {
	fd.srv.Register(method, func(_ context.Context, cs *ipc.ConnState, p json.RawMessage) (any, error) {
		fd.mu.Lock()
		fd.calls = append(fd.calls, method+" "+string(p))
		fd.mu.Unlock()
		return fn(cs, p)
	}, gate)
}

func (fd *fakeDaemon) reply(method string, gate ipc.Gate, result any) {
	fd.handle(method, gate, func(*ipc.ConnState, json.RawMessage) (any, error) { return result, nil })
}

func (fd *fakeDaemon) isKilled() bool {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return fd.killed
}

func (fd *fakeDaemon) setKilled(k bool) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.killed = k
}

// called returns the recorded calls of method, params only.
func (fd *fakeDaemon) called(method string) []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	var out []string
	for _, c := range fd.calls {
		if m, p, _ := strings.Cut(c, " "); m == method {
			out = append(out, p)
		}
	}
	return out
}

// ui is a Launcher over a fakeDaemon, on the daemon's fake clock.
type ui struct {
	t      *testing.T
	clock  *core.FakeClock
	fd     *fakeDaemon
	l      *Launcher
	cancel context.CancelFunc
	pipes  atomic.Int64 // daemon connections the UI holds open
}

// countedCaller counts the UI's open daemon connections.
type countedCaller struct {
	Caller
	once  sync.Once
	pipes *atomic.Int64
}

func (c *countedCaller) Close() error {
	c.once.Do(func() { c.pipes.Add(-1) })
	return c.Caller.Close()
}

func newUI(t *testing.T, fd *fakeDaemon) *ui {
	t.Helper()
	clock := fd.clock
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	u := &ui{t: t, clock: clock, fd: fd, cancel: cancel}
	dial := func() (Caller, error) {
		c, _ := fd.srv.Pipe(ctx)
		u.pipes.Add(1)
		return &countedCaller{Caller: c, pipes: &u.pipes}, nil
	}
	u.l = NewLauncher(ctx, Options{Clock: clock, Dial: dial})
	return u
}

// launchURL asks for a new launch URL.
func (u *ui) launchURL() string {
	u.t.Helper()
	s, err := u.l.Start(context.Background())
	if err != nil {
		u.t.Fatal(err)
	}
	return s
}

// browser is an HTTP client that keeps one cookie and never follows
// redirects.
type browser struct {
	t      *testing.T
	base   string
	cookie *http.Cookie
	client *http.Client
}

type resp struct {
	code   int
	header http.Header
	body   string
}

func newBrowser(t *testing.T, launch string) *browser {
	u, err := url.Parse(launch)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, base: "http://" + u.Host, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// open launches a browser session and returns it with the cookie set.
func (u *ui) open() *browser {
	u.t.Helper()
	launch := u.launchURL()
	b := newBrowser(u.t, launch)
	r := b.do("GET", launch, nil, nil)
	if r.code != http.StatusSeeOther {
		u.t.Fatalf("launch: %d %s", r.code, r.body)
	}
	return b
}

// do sends a request. A relative target is on the UI's origin. It sends
// the cookie when set and records a new one.
func (b *browser) do(method, target string, form url.Values, edit func(*http.Request)) resp {
	b.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = b.base + target
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	if edit != nil {
		edit(req)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	for _, c := range res.Cookies() {
		b.cookie = c
	}
	return resp{code: res.StatusCode, header: res.Header, body: string(raw)}
}

func (b *browser) get(path string) resp { b.t.Helper(); return b.do("GET", path, nil, nil) }

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// csrf reads the form token from a page.
func (b *browser) csrf(page string) string {
	b.t.Helper()
	r := b.get(page)
	m := csrfField.FindStringSubmatch(r.body)
	if m == nil {
		b.t.Fatalf("no csrf field on %s (%d): %s", page, r.code, r.body)
	}
	return m[1]
}

// post submits a form from page like a browser would: with the page's CSRF
// token and this origin.
func (b *browser) post(page, action string, form url.Values) resp {
	b.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", b.csrf(page))
	return b.do("POST", action, form, func(r *http.Request) { r.Header.Set("Origin", b.base) })
}

// follow posts and then loads the page the action redirected to.
func (b *browser) follow(page, action string, form url.Values) resp {
	b.t.Helper()
	r := b.post(page, action, form)
	if r.code != http.StatusSeeOther {
		b.t.Fatalf("%s: %d %s", action, r.code, r.body)
	}
	return b.get(r.header.Get("Location"))
}

func wantContains(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Errorf("page lacks %q:\n%s", p, body)
		}
	}
}
