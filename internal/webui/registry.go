package webui

import (
	"context"
	"net/http"
	"strings"

	"github.com/cravv/cravv-connect/internal/ipc"
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
}

// Call calls the daemon on this browser session's connection.
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

// WithPassword runs fn, an action the daemon gates behind the password. A
// password typed into the form's "password" field first unlocks this
// browser session's connection; the daemon's Guard checks it with the same
// rate limit and lockout as the CLI. Without one, fn runs as is and fails
// with the daemon's auth_required unless the window is still open.
func (rq *Request) WithPassword(ctx context.Context, fn func() error) error {
	if pw := rq.HTTP.PostFormValue("password"); pw != "" {
		var r ipc.UnlockResult
		if err := rq.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: pw}, &r); err != nil {
			return err
		}
		rq.sess.setUnlocked(r.ExpiresAt)
	}
	return fn()
}
