package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// maxFormBytes caps a form body.
const maxFormBytes = 64 << 10

// contentSecurityPolicy allows only this origin's own scripts, styles and
// forms, and no framing.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// server is the HTTP handler of one running UI instance.
type server struct {
	clock  core.Clock
	dial   Dialer
	pages  *Registry
	log    *slog.Logger
	port   int
	cookie string
	hosts  [2]string
	views  *views
	mux    *http.ServeMux

	mu       sync.Mutex
	last     time.Time
	tokens   map[string]time.Time // launch token -> when it was minted
	sessions map[string]*uiSession
	order    []string // session IDs, oldest first
}

func newServer(o Options, port int) (*server, error) {
	v, err := loadViews()
	if err != nil {
		return nil, err
	}
	s := &server{
		clock: o.Clock, dial: o.Dial, pages: o.Pages, log: o.Logger, port: port,
		cookie: "cravv_ui_" + strconv.Itoa(port),
		hosts:  [2]string{"127.0.0.1:" + strconv.Itoa(port), "localhost:" + strconv.Itoa(port)},
		views:  v, last: o.Clock.Now(),
		tokens: map[string]time.Time{}, sessions: map[string]*uiSession{},
	}
	static, err := fs.Sub(assets, "assets/static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	// Not "GET /launch": that pattern also answers HEAD, and a HEAD (a link
	// preview or prefetch) must not use up the one-time token.
	mux.HandleFunc("/launch", s.launch)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.home)
	for _, p := range o.Pages.Pages() {
		mux.HandleFunc("GET "+p.Path, s.page(p))
	}
	for _, a := range o.Pages.Actions() {
		mux.HandleFunc("POST "+a.Path, s.action(a))
	}
	s.mux = mux
	return s, nil
}

// ServeHTTP runs the checks every request gets, then routes it.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.touch()
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	// same-origin, not no-referrer: under no-referrer browsers send
	// "Origin: null" on the UI's own form posts, and sameOrigin refuses
	// those. Other sites still get no Referer.
	h.Set("Referrer-Policy", "same-origin")
	if r.Host != s.hosts[0] && r.Host != s.hosts[1] {
		// A page on another name that resolves here (DNS rebinding) never
		// gets an answer.
		s.deny(w, http.StatusForbidden, "This address is not the cravv-connect UI.")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *server) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = s.clock.Now()
}

func (s *server) lastRequest() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// randomToken returns 32 random bytes, base64url.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ErrTooManyLaunches refuses ui.start while MaxLaunchTokens unused launch
// links are all younger than LaunchTokenGrace. It is an ipc.ErrBusy, so it
// reaches the CLI as kind "busy" with its own wording.
var ErrTooManyLaunches error = busyError("too many unused web UI links; wait a few seconds and run cravv-connect ui again")

type busyError string

func (e busyError) Error() string { return string(e) }
func (busyError) Unwrap() error   { return ipc.ErrBusy }

// mintToken adds a launch token that works once within LaunchTokenTTL. At
// MaxLaunchTokens it drops the oldest token only if that one is at least
// LaunchTokenGrace old, so a burst of ui.start calls cannot knock out a
// link a browser is about to open; otherwise it refuses.
func (s *server) mintToken() (string, error) {
	tok, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	for t, minted := range s.tokens {
		if now.Sub(minted) >= LaunchTokenTTL {
			delete(s.tokens, t)
		}
	}
	if len(s.tokens) >= MaxLaunchTokens {
		var oldest string
		for t, minted := range s.tokens {
			if oldest == "" || minted.Before(s.tokens[oldest]) {
				oldest = t
			}
		}
		if now.Sub(s.tokens[oldest]) < LaunchTokenGrace {
			return "", ErrTooManyLaunches
		}
		delete(s.tokens, oldest)
	}
	s.tokens[tok] = now
	return tok, nil
}

// takeToken consumes tok if it is a live launch token.
func (s *server) takeToken(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	for t, minted := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			delete(s.tokens, t)
			return now.Sub(minted) < LaunchTokenTTL
		}
	}
	return false
}

// launch swaps a launch token for a session cookie and redirects to a URL
// without the token.
func (s *server) launch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		s.deny(w, http.StatusMethodNotAllowed, "Open the launch link with GET.")
		return
	}
	if !s.takeToken(r.URL.Query().Get("token")) {
		s.deny(w, http.StatusForbidden, "This link was already used or has expired. Run cravv-connect ui again for a new one.")
		return
	}
	_, id, err := s.newSession()
	if err != nil {
		s.log.Warn("web UI session", "err", err)
		s.deny(w, http.StatusServiceUnavailable, "Could not reach the daemon. Run cravv-connect ui again.")
		return
	}
	s.setCookie(w, id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// newSession opens a daemon connection for a new browser session, ending
// the oldest session beyond MaxSessions.
func (s *server) newSession() (*uiSession, string, error) {
	id, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	c, err := s.dial()
	if err != nil {
		return nil, "", err
	}
	sess := newUISession(c, csrf)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = sess
	s.order = append(s.order, id)
	for len(s.order) > MaxSessions {
		old := s.order[0]
		s.order = s.order[1:]
		if o, ok := s.sessions[old]; ok {
			o.end()
			delete(s.sessions, old)
		}
	}
	return sess, id, nil
}

func (s *server) setCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// session returns the browser session named by the request's cookie and
// its cookie ID.
func (s *server) session(r *http.Request) (*uiSession, string) {
	ck, err := r.Cookie(s.cookie)
	if err != nil {
		return nil, ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[ck.Value]
	if sess == nil {
		return nil, ""
	}
	return sess, ck.Value
}

// rotate gives a session a new cookie ID and CSRF token after a password
// action, so a copy of the old cookie or token (read by another local
// process) stops working.
func (s *server) rotate(w http.ResponseWriter, sess *uiSession, oldID string) {
	id, err := randomToken()
	if err != nil {
		s.log.Warn("web UI session rotation", "err", err)
		return
	}
	csrf, err := randomToken()
	if err != nil {
		s.log.Warn("web UI session rotation", "err", err)
		return
	}
	s.mu.Lock()
	if s.sessions[oldID] != sess {
		// Ended meanwhile.
		s.mu.Unlock()
		return
	}
	delete(s.sessions, oldID)
	s.sessions[id] = sess
	for i, o := range s.order {
		if o == oldID {
			s.order[i] = id
		}
	}
	s.mu.Unlock()
	sess.setCSRF(csrf)
	s.setCookie(w, id)
}

// sweepFlows ends every session's expired pairings.
func (s *server) sweepFlows() {
	s.mu.Lock()
	list := make([]*uiSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		list = append(list, sess)
	}
	s.mu.Unlock()
	now := s.clock.Now()
	for _, sess := range list {
		sess.sweepFlows(now)
	}
}

func (s *server) closeSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		sess.end()
		delete(s.sessions, id)
	}
	s.order = nil
	clear(s.tokens)
}

// sameOrigin reports whether the request's Origin is this UI's origin.
// Browsers send Origin on every POST; a missing one is refused.
func (s *server) sameOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == "http://"+r.Host
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	if sess, _ := s.session(r); sess == nil {
		s.denyNoSession(w)
		return
	}
	pages := s.pages.Pages()
	if len(pages) == 0 {
		s.deny(w, http.StatusNotFound, "No pages.")
		return
	}
	http.Redirect(w, r, pages[0].Path, http.StatusSeeOther)
}

func (s *server) denyNoSession(w http.ResponseWriter) {
	s.deny(w, http.StatusUnauthorized, "This browser has no cravv-connect session, or it ended. Run cravv-connect ui in a terminal to open the UI.")
}

// deny answers with a plain text error.
func (s *server) deny(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, msg)
}

// page serves a page's GET view.
func (s *server) page(p Page) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, _ := s.session(r)
		if sess == nil {
			s.denyNoSession(w)
			return
		}
		rq := &Request{HTTP: r, sess: sess, srv: s}
		data, err := p.Load(r.Context(), rq)
		s.render(w, rq, p.Path, p.Title, p.Template, data, err)
	}
}

// action runs a state-changing POST: it needs the session cookie, the
// session's CSRF token and a matching Origin. Once a password was accepted
// the session gets a new cookie and CSRF token.
func (s *server) action(a Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, id := s.session(r)
		if sess == nil {
			s.denyNoSession(w)
			return
		}
		if !s.sameOrigin(r) {
			s.deny(w, http.StatusForbidden, "Cross-site request refused.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			s.deny(w, http.StatusBadRequest, "Bad form.")
			return
		}
		if !sess.csrfOK(r.PostForm.Get("csrf")) {
			s.deny(w, http.StatusForbidden, "Missing or wrong form token. Reload the page and try again.")
			return
		}
		rq := &Request{HTTP: r, sess: sess, srv: s}
		rep, err := a.Run(r.Context(), rq)
		if rq.passwordOK {
			s.rotate(w, sess, id)
		}
		if err != nil && rep.Template != "" {
			// A step of a multi-step flow shows its own error in place.
			s.render(w, rq, a.Back, rep.Title, rep.Template, rep.Data, err)
			return
		}
		if err != nil {
			sess.addFlash(flashError, message(err))
			http.Redirect(w, r, a.Back, http.StatusSeeOther)
			return
		}
		if rep.Template != "" {
			s.render(w, rq, a.Back, rep.Title, rep.Template, rep.Data, nil)
			return
		}
		if rep.Notice != "" {
			sess.addFlash(flashOK, rep.Notice)
		}
		to := rep.To
		if to == "" {
			to = a.Back
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
}
