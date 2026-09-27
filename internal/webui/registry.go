package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// Page is one navigation entry and its GET view.
type Page struct {
	Path     string // such as "/devices"
	Title    string // navigation label and heading
	Template string // file under assets/templates that defines "content"
	// Load fetches the page's data over the browser session's IPC
	// connection. An error is shown above the page, which renders with the
	// data returned alongside it (often nil).
	Load func(ctx context.Context, rq *Request) (any, error)
}

// Action is one state-changing POST. The server checks the session cookie,
// the CSRF token and Origin before Run.
type Action struct {
	Path string // such as "/devices/pause"
	Back string // the page to return to, also after an error
	Run  func(ctx context.Context, rq *Request) (Reply, error)
}

// Reply is what an action returns: a redirect (to To, or Back) with an
// optional notice, or, when Template is set, a page rendered in place (for
// multi-step flows such as pairing).
type Reply struct {
	To       string
	Notice   string
	Template string
	Title    string
	Data     any
}

// Registry holds the pages and actions the UI serves. A new page is one new
// file with an add function plus one line in DefaultRegistry.
type Registry struct {
	pages   []Page
	actions []Action
}

// AddPage adds a page to the navigation, after the ones added before.
func (r *Registry) AddPage(p Page) { r.pages = append(r.pages, p) }

// AddAction adds a POST action.
func (r *Registry) AddAction(a Action) { r.actions = append(r.actions, a) }

// Pages returns the pages in navigation order.
func (r *Registry) Pages() []Page { return append([]Page(nil), r.pages...) }

// Actions returns the actions.
func (r *Registry) Actions() []Action { return append([]Action(nil), r.actions...) }

// DefaultRegistry returns the built-in pages in navigation order.
func DefaultRegistry() *Registry {
	r := &Registry{}
	for _, add := range []func(*Registry){
		addDevices,
		addSessions,
		addApprovals,
		addManaged,
		addActivity,
		addStatus,
	} {
		add(r)
	}
	return r
}

// Request is one page load or action for a browser session.
type Request struct {
	HTTP *http.Request
	sess *uiSession
	srv  *server
	// passwordOK is set once the daemon accepted a password in this
	// request; the server then rotates the session's cookie and CSRF token.
	passwordOK bool
}

// Conn is a daemon connection a password action runs on.
type Conn interface {
	Call(ctx context.Context, method string, params, result any) error
}

// errFlowEnded is returned for a pairing step whose flow is gone (finished,
// expired, ended with its browser session, or never this session's).
var errFlowEnded = errors.New("this pairing ended")

// Call calls the daemon on this browser session's connection, which is
// never unlocked.
func (rq *Request) Call(ctx context.Context, method string, params, result any) error {
	return rq.sess.c.Call(ctx, method, params, result)
}

// Form returns a trimmed form value of a POST.
func (rq *Request) Form(name string) string {
	return strings.TrimSpace(rq.HTTP.PostFormValue(name))
}

// Query returns a trimmed query parameter.
func (rq *Request) Query(name string) string {
	return strings.TrimSpace(rq.HTTP.URL.Query().Get(name))
}

// WithPassword runs fn, an action the daemon gates behind the password, on
// a fresh daemon connection unlocked with the password typed into this
// request's "password" field, and closes that connection afterwards. No
// unlock outlives the request. The daemon's Guard checks the password with
// the same rate limit and lockout as the CLI; an empty field is refused
// here without counting as an attempt.
func (rq *Request) WithPassword(ctx context.Context, fn func(c Conn) error) error {
	c, err := rq.unlocked(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	return fn(c)
}

// unlocked opens a daemon connection and unlocks it with the request's
// password.
func (rq *Request) unlocked(ctx context.Context) (Caller, error) {
	pw := rq.HTTP.PostFormValue("password")
	if pw == "" {
		return nil, core.ErrAuthRequired
	}
	c, err := rq.srv.dial()
	if err != nil {
		return nil, err
	}
	if err := unlock(ctx, c, pw); err != nil {
		c.Close()
		return nil, err
	}
	rq.passwordOK = true
	return c, nil
}

func unlock(ctx context.Context, c Conn, pw string) error {
	var r ipc.UnlockResult
	return c.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: pw}, &r)
}

// StartFlow begins a pairing, which spans several requests: start runs on a
// new daemon connection unlocked with this request's password and returns
// the daemon's pending ID. The connection stays open for the flow's later
// steps until FinishFlow, PairFlowTTL, or the end of the browser session.
// It returns the flow ID for the page.
func (rq *Request) StartFlow(ctx context.Context, start func(c Conn) (pendingID string, err error)) (string, error) {
	c, err := rq.unlocked(ctx)
	if err != nil {
		return "", err
	}
	pending, err := start(c)
	if err != nil {
		c.Close()
		return "", err
	}
	id, err := randomToken()
	if err != nil {
		c.Close()
		return "", err
	}
	f := &pairFlow{c: c, pendingID: pending, expires: rq.srv.clock.Now().Add(PairFlowTTL)}
	if !rq.sess.addFlow(id, f) {
		c.Close()
		return "", ipc.ErrClosed
	}
	return id, nil
}

// FlowStep runs a later step of this session's pairing called by the
// form's "flow" field on the pairing's connection, with its pending ID. If
// needPassword, the request's password must unlock that connection again
// first. It returns the flow ID.
func (rq *Request) FlowStep(ctx context.Context, needPassword bool, step func(c Conn, pendingID string) error) (string, error) {
	id := rq.Form("flow")
	f := rq.sess.flow(id, rq.srv.clock.Now())
	if f == nil {
		return id, errFlowEnded
	}
	if needPassword {
		pw := rq.HTTP.PostFormValue("password")
		if pw == "" {
			return id, core.ErrAuthRequired
		}
		if err := unlock(ctx, f.c, pw); err != nil {
			return id, err
		}
		rq.passwordOK = true
	}
	return id, step(f.c, f.pendingID)
}

// EndFlow ends this session's pairing called id and closes its connection.
func (rq *Request) EndFlow(id string) { rq.sess.endFlow(id) }
