package webui

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// assets holds the templates and static files; no build step, no external
// requests.
//
//go:embed assets
var assets embed.FS

// views holds one template set per page template: the layout plus the file
// that defines "content".
type views struct {
	byName map[string]*template.Template
}

var funcs = template.FuncMap{
	"time":  fmtTime,
	"short": shortID,
	"peer":  peerText,
	"clean": cleanLine,
	"dash":  dash,
}

func loadViews() (*views, error) {
	base, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "assets/templates/layout.html")
	if err != nil {
		return nil, err
	}
	files, err := fs.Glob(assets, "assets/templates/*.html")
	if err != nil {
		return nil, err
	}
	v := &views{byName: map[string]*template.Template{}}
	for _, f := range files {
		name := path.Base(f)
		if name == "layout.html" {
			continue
		}
		t, err := template.Must(base.Clone()).ParseFS(assets, f)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		v.byName[name] = t
	}
	return v, nil
}

type navItem struct {
	Path, Title string
	Current     bool
}

// view is what the layout renders.
type view struct {
	Title string
	Nav   []navItem
	Flash []flash
	Error string
	CSRF  string
	Data  any
}

// render writes a page, or a plain 500 if the template fails.
func (s *server) render(w http.ResponseWriter, rq *Request, current, title, tmpl string, data any, loadErr error) {
	t, ok := s.views.byName[tmpl]
	if !ok {
		s.deny(w, http.StatusInternalServerError, "Unknown page.")
		return
	}
	v := view{Title: title, Flash: rq.sess.takeFlashes(), CSRF: rq.sess.csrfToken(), Data: data}
	for _, p := range s.pages.Pages() {
		v.Nav = append(v.Nav, navItem{Path: p.Path, Title: p.Title, Current: p.Path == current})
	}
	if loadErr != nil {
		v.Error = message(loadErr)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.log.Warn("web UI render", "template", tmpl, "err", err)
		s.deny(w, http.StatusInternalServerError, "The page could not be shown.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// message is the text shown for an error. Daemon errors keep their own
// wording (they name what to do); the password ones are reworded for a page.
func message(err error) string {
	switch {
	case errors.Is(err, core.ErrBadPassword):
		return "Incorrect password."
	case errors.Is(err, core.ErrLocked):
		return fmt.Sprintf("Too many wrong passwords. Password actions are locked for %d minutes.", int(core.LockoutDuration.Minutes()))
	case errors.Is(err, core.ErrAuthRequired):
		return "This needs your login password."
	case errors.Is(err, core.ErrKilled):
		return "The kill switch is on. Resume on the Status page first."
	case errors.Is(err, ipc.ErrClosed):
		return "The connection to the daemon ended. Run cravv-connect ui again."
	case errors.Is(err, errFlowEnded):
		return "This pairing ended. Start again on the Devices page."
	}
	return cleanLine(err.Error())
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func shortID(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
